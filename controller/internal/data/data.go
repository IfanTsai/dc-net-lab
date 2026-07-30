// Package data implements the data layer: it owns all database
// access and the container runtime driver, mirroring kratos-layout's
// data package. One file per functional module (lab, topology, plan,
// operation, runtime) implements the repo interfaces defined in biz;
// resources are stored in SQLite as JSON documents with indexed key
// columns, and biz never opens connections.
package data

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/wire"
	_ "modernc.org/sqlite"

	"github.com/ifantsai/dcnetlab/controller/internal/biz"
	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// ProviderSet is the data-layer providers.
var ProviderSet = wire.NewSet(
	NewData,
	NewLabRepo,
	NewTopologyRepo,
	NewPlanRepo,
	NewOperationRepo,
	NewPowerRepo,
	NewProgramRepo,
	NewProgramAgent,
	NewPackageRepo,
	NewTrafficRepo,
	NewTrafficAgent,
	NewTrafficStore,
	NewFaultRepo,
	NewCaptureRepo,
	NewObserverStore,
	NewMetricsStore,
	NewMetricsAgent,
	NewOperationStore,
	NewRuntimeDriver,
)

// Data wraps the SQLite database shared by the repo implementations
// plus the on-disk artifact directory (package payloads).
type Data struct {
	db  *sql.DB
	dir string
}

const schema = `
CREATE TABLE IF NOT EXISTS labs (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS nodes (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	name TEXT NOT NULL,
	doc TEXT NOT NULL,
	UNIQUE(lab_id, name)
);
CREATE INDEX IF NOT EXISTS idx_nodes_lab ON nodes(lab_id);
CREATE TABLE IF NOT EXISTS links (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	name TEXT NOT NULL,
	doc TEXT NOT NULL,
	UNIQUE(lab_id, name)
);
CREATE INDEX IF NOT EXISTS idx_links_lab ON links(lab_id);
CREATE TABLE IF NOT EXISTS plans (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS operations (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_operations_lab ON operations(lab_id);
CREATE TABLE IF NOT EXISTS generations (
	lab_id TEXT NOT NULL,
	generation INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	desired_state TEXT NOT NULL,
	PRIMARY KEY(lab_id, generation)
);
CREATE TABLE IF NOT EXISTS allocations (
	lab_id TEXT NOT NULL,
	pool TEXT NOT NULL,
	value TEXT NOT NULL,
	owner TEXT NOT NULL,
	PRIMARY KEY(lab_id, pool, value)
);
CREATE TABLE IF NOT EXISTS programs (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	name TEXT NOT NULL,
	server_name TEXT NOT NULL DEFAULT '',
	doc TEXT NOT NULL,
	UNIQUE(lab_id, name, server_name)
);
CREATE INDEX IF NOT EXISTS idx_programs_lab ON programs(lab_id);
CREATE TABLE IF NOT EXISTS packages (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	version TEXT NOT NULL,
	doc TEXT NOT NULL,
	UNIQUE(name, version)
);
CREATE TABLE IF NOT EXISTS traffic_scenarios (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	name TEXT NOT NULL,
	doc TEXT NOT NULL,
	UNIQUE(lab_id, name)
);
CREATE INDEX IF NOT EXISTS idx_traffic_scenarios_lab ON traffic_scenarios(lab_id);
CREATE TABLE IF NOT EXISTS fault_scenarios (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	name TEXT NOT NULL,
	doc TEXT NOT NULL,
	UNIQUE(lab_id, name)
);
CREATE INDEX IF NOT EXISTS idx_fault_scenarios_lab ON fault_scenarios(lab_id);
CREATE TABLE IF NOT EXISTS capture_sessions (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	name TEXT NOT NULL,
	doc TEXT NOT NULL,
	UNIQUE(lab_id, name)
);
CREATE INDEX IF NOT EXISTS idx_capture_sessions_lab ON capture_sessions(lab_id);
`

// NewData opens the SQLite database under the configured data
// directory; the cleanup closes it on shutdown.
func NewData(c *conf.Data, log *slog.Logger) (*Data, func(), error) {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return nil, nil, err
	}

	d, err := open(filepath.Join(c.Dir, "dcnetlab.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}

	d.dir = c.Dir
	if err := d.dropRenamedBuiltin(); err != nil {
		_ = d.Close()

		return nil, nil, fmt.Errorf("drop renamed builtin package: %w", err)
	}

	cleanup := func() {
		if err := d.Close(); err != nil {
			log.Error("close database", "error", err)
		}
	}

	return d, cleanup, nil
}

// open opens (and migrates) the database at path. Use ":memory:" for
// tests; NewData wires it into the application.
func open(path string) (*Data, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}

	// SQLite handles one writer at a time; serialise access through
	// a single connection to avoid SQLITE_BUSY in this local tool.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	if err := migrateProgramServerName(db); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("migrate program server names: %w", err)
	}

	if err := migrateLegacyPrograms(db); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("migrate legacy programs: %w", err)
	}

	return &Data{db: db}, nil
}

// migrateProgramServerName rebuilds the programs table when it still
// carries the pre-batch UNIQUE(lab_id, name) constraint: batch
// deployment creates one program per server under the same name, so
// uniqueness is per (lab, name, server). SQLite cannot alter
// constraints in place; the table is copied over.
func migrateProgramServerName(db *sql.DB) error {
	var withServerName int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('programs') WHERE name = 'server_name'`).
		Scan(&withServerName)
	if err != nil {
		return err
	}

	if withServerName > 0 {
		return nil
	}

	rows, err := db.Query(`SELECT id, lab_id, name, doc FROM programs`)
	if err != nil {
		return err
	}

	type row struct{ id, labID, name, doc, serverName string }
	var programs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.labID, &r.name, &r.doc); err != nil {
			_ = rows.Close()

			return err
		}

		var p model.Program
		if err := json.Unmarshal([]byte(r.doc), &p); err == nil {
			r.serverName = p.Spec.ServerName
		}

		programs = append(programs, r)
	}

	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := db.Exec(`
CREATE TABLE programs_new (
	id TEXT PRIMARY KEY,
	lab_id TEXT NOT NULL,
	name TEXT NOT NULL,
	server_name TEXT NOT NULL DEFAULT '',
	doc TEXT NOT NULL,
	UNIQUE(lab_id, name, server_name)
);`); err != nil {
		return err
	}

	for _, r := range programs {
		if _, err := db.Exec(`INSERT INTO programs_new (id, lab_id, name, server_name, doc) VALUES (?, ?, ?, ?, ?)`,
			r.id, r.labID, r.name, r.serverName, r.doc); err != nil {
			return err
		}
	}

	if _, err := db.Exec(`
DROP TABLE programs;
ALTER TABLE programs_new RENAME TO programs;
CREATE INDEX IF NOT EXISTS idx_programs_lab ON programs(lab_id);`); err != nil {
		return err
	}

	return nil
}

// legacyBuiltinName is the builtin package's name before it was
// renamed to trafficgen; migrations rewrite references and drop the
// stale package row.
const legacyBuiltinName = "lab-app"

// dropRenamedBuiltin removes the pre-rename builtin package row and
// its payload; registerBuiltin recreates it under the new name.
func (s *Data) dropRenamedBuiltin() error {
	res, err := s.db.Exec(`DELETE FROM packages WHERE name = ?`, legacyBuiltinName)
	if err != nil {
		return err
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}

	return os.RemoveAll(filepath.Join(s.dir, "packages", legacyBuiltinName))
}

// migrateLegacyPrograms rewrites outdated program documents: the
// pre-package "mode" form becomes a builtin package reference (mode
// as first argument), and references to the builtin package's old
// name are renamed.
func migrateLegacyPrograms(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, doc FROM programs`)
	if err != nil {
		return err
	}

	defer rows.Close()

	updates := make(map[string]string)
	for rows.Next() {
		var id, doc string
		if err := rows.Scan(&id, &doc); err != nil {
			return err
		}

		migrated, changed, err := migrateProgramDoc(doc)
		if err != nil {
			return fmt.Errorf("program %s: %w", id, err)
		}

		if changed {
			updates[id] = migrated
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}

	for id, doc := range updates {
		if _, err := db.Exec(`UPDATE programs SET doc = ? WHERE id = ?`, doc, id); err != nil {
			return err
		}
	}

	return nil
}

// migrateProgramDoc converts one legacy program document; changed
// reports whether a rewrite happened.
func migrateProgramDoc(doc string) (string, bool, error) {
	var v map[string]any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		return "", false, err
	}

	spec, ok := v["spec"].(map[string]any)
	if !ok {
		return "", false, nil
	}

	mode, _ := spec["mode"].(string)
	name, _ := spec["packageName"].(string)
	switch {
	case name == "" && mode != "":
		args := []any{mode}
		if rest, ok := spec["args"].([]any); ok {
			args = append(args, rest...)
		}

		spec["packageName"] = model.BuiltinPackageName
		spec["packageVersion"] = model.BuiltinPackageVersion
		spec["entrypoint"] = model.BuiltinPackageEntrypoint
		spec["args"] = args
		delete(spec, "mode")
	case name == legacyBuiltinName:
		spec["packageName"] = model.BuiltinPackageName
		spec["entrypoint"] = model.BuiltinPackageEntrypoint
	default:
		return "", false, nil
	}

	out, err := json.Marshal(v)
	if err != nil {
		return "", false, err
	}

	return string(out), true, nil
}

// Close closes the database.
func (s *Data) Close() error { return s.db.Close() }

// --- shared helpers ---

func marshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

func (s *Data) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		tx.Rollback()

		return err
	}

	return tx.Commit()
}

func checkFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if n == 0 {
		return biz.ErrNotFound
	}

	return nil
}

func getDoc[T any](s *Data, query string, args ...any) (*T, error) {
	var doc string
	err := s.db.QueryRow(query, args...).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, biz.ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	var v T
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		return nil, err
	}

	return &v, nil
}

func listDocs[T any](s *Data, query string, args ...any) ([]*T, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var out []*T
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}

		var v T
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			return nil, err
		}

		out = append(out, &v)
	}

	return out, rows.Err()
}
