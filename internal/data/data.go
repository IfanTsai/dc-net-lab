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

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/conf"
)

// ProviderSet is the data-layer providers.
var ProviderSet = wire.NewSet(
	NewData,
	NewLabRepo,
	NewTopologyRepo,
	NewPlanRepo,
	NewOperationRepo,
	NewPowerRepo,
	NewObserverStore,
	NewOperationStore,
	NewRuntimeDriver,
)

// Data wraps the SQLite database shared by the repo implementations.
type Data struct {
	db *sql.DB
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

	return &Data{db: db}, nil
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
