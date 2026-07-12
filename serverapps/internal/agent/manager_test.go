package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/agentapi"
)

// testPackage is the fake package every test program runs out of;
// its entrypoint is a shell wrapper so specs control the process
// behaviour via one script argument (sh -c "$1").
const (
	testPackageName    = "testpkg"
	testPackageVersion = "1.0.0"
	testEntrypoint     = "run.sh"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()

	m := newManagerAt(t, t.TempDir())
	t.Cleanup(m.Shutdown)

	return m
}

// newManagerAt builds a manager over dir with the test package
// planted in its store.
func newManagerAt(t *testing.T, dir string) *Manager {
	t.Helper()

	m, err := NewManager(dir, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	plantTestPackage(t, dir)

	return m
}

// plantTestPackage writes the shell-wrapper package directly into
// the store, bypassing download.
func plantTestPackage(t *testing.T, dir string) {
	t.Helper()

	pkgDir := filepath.Join(dir, "packages", testPackageName, testPackageVersion)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	script := "#!/bin/sh\nexec sh -c \"$1\"\n"
	if err := os.WriteFile(filepath.Join(pkgDir, testEntrypoint), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func shSpec(name, script, policy string) Spec {
	return Spec{
		Name:           name,
		PackageName:    testPackageName,
		PackageVersion: testPackageVersion,
		Entrypoint:     testEntrypoint,
		Args:           []string{script},
		RestartPolicy:  policy,
	}
}

// waitState polls until the program reaches the wanted state.
func waitState(t *testing.T, m *Manager, name, want string) Info {
	t.Helper()

	var last Info
	for range 100 {
		for _, info := range m.List() {
			if info.Spec.Name == name {
				last = info
			}
		}

		if last.State == want {
			return last
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("program %s: state %s, want %s (%+v)", name, last.State, want, last)

	return last
}

func TestInstallValidation(t *testing.T) {
	m := newTestManager(t)

	tests := []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{name: "valid", spec: shSpec("app-1", "true", agentapi.RestartNever)},
		{name: "bad name", spec: shSpec("../escape", "true", agentapi.RestartNever), wantErr: true},
		{name: "bad policy", spec: withPolicy(shSpec("app-2", "true", ""), "Sometimes"), wantErr: true},
		{name: "default policy", spec: shSpec("app-3", "true", "")},
		{name: "missing package", spec: withPackage(shSpec("app-4", "true", agentapi.RestartNever), "nosuch", "9.9.9"), wantErr: true},
		{name: "escaping entrypoint", spec: withEntrypoint(shSpec("app-5", "true", agentapi.RestartNever), "../../bin/sh"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := m.Install(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}

			if err == nil && info.State != agentapi.StateConfigured {
				t.Errorf("state=%s, want Configured", info.State)
			}
		})
	}
}

func withPolicy(s Spec, policy string) Spec {
	s.RestartPolicy = policy

	return s
}

func withPackage(s Spec, name, version string) Spec {
	s.PackageName, s.PackageVersion = name, version

	return s
}

func withEntrypoint(s Spec, entrypoint string) Spec {
	s.Entrypoint = entrypoint

	return s
}

func TestStartStopLifecycle(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.Install(shSpec("sleeper", "sleep 60", agentapi.RestartNever)); err != nil {
		t.Fatal(err)
	}

	info, err := m.Start("sleeper")
	if err != nil {
		t.Fatal(err)
	}

	if info.State != agentapi.StateRunning || info.PID == 0 {
		t.Fatalf("after start: %+v", info)
	}

	info, err = m.Stop("sleeper")
	if err != nil {
		t.Fatal(err)
	}

	if info.State != agentapi.StateStopped || info.PID != 0 {
		t.Fatalf("after stop: %+v", info)
	}
}

func TestRestartPolicyAlways(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.Install(shSpec("flappy", "exit 0", agentapi.RestartAlways)); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Start("flappy"); err != nil {
		t.Fatal(err)
	}

	// The process exits immediately; Always must restart it.
	deadline := time.After(5 * time.Second)
	for {
		var restarts int
		for _, info := range m.List() {
			if info.Spec.Name == "flappy" {
				restarts = info.Restarts
			}
		}

		if restarts >= 1 {
			break
		}

		select {
		case <-deadline:
			t.Fatal("program was not restarted")
		case <-time.After(20 * time.Millisecond):
		}
	}

	if _, err := m.Stop("flappy"); err != nil {
		t.Fatal(err)
	}
}

func TestNeverPolicyFailure(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.Install(shSpec("oneshot", "exit 3", agentapi.RestartNever)); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Start("oneshot"); err != nil {
		t.Fatal(err)
	}

	info := waitState(t, m, "oneshot", agentapi.StateFailed)
	if !strings.Contains(info.LastError, "exit status 3") {
		t.Errorf("last error: %q", info.LastError)
	}
}

func TestLogsAndRemove(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.Install(shSpec("echoer", "echo one; echo two; sleep 60", agentapi.RestartNever)); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Start("echoer"); err != nil {
		t.Fatal(err)
	}

	var logs string
	for range 100 {
		var err error
		logs, err = m.TailLogs("echoer", 10)
		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(logs, "two") {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	if logs != "one\ntwo\n" {
		t.Errorf("logs=%q", logs)
	}

	if tail, _ := m.TailLogs("echoer", 1); tail != "two\n" {
		t.Errorf("tail 1: %q", tail)
	}

	if err := m.Remove("echoer"); err != nil {
		t.Fatal(err)
	}

	if _, err := m.TailLogs("echoer", 1); err == nil {
		t.Error("logs should fail after remove")
	}
}

func TestManagerRestoresDesiredState(t *testing.T) {
	dir := t.TempDir()

	// First agent life: install and start, then "crash" (no Shutdown).
	// Never policy keeps the first supervisor from racing the second
	// manager for restarts once its process is killed.
	m1 := newManagerAt(t, dir)

	if _, err := m1.Install(shSpec("svc", "sleep 60", agentapi.RestartNever)); err != nil {
		t.Fatal(err)
	}

	if _, err := m1.Start("svc"); err != nil {
		t.Fatal(err)
	}

	// Second life on the same state dir: the stale process is killed
	// and the program restarted because its desired state is running.
	m2, err := NewManager(dir, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	defer m2.Shutdown()

	info := waitState(t, m2, "svc", agentapi.StateRunning)
	if info.PID == 0 {
		t.Fatalf("restored program not running: %+v", info)
	}
}

func TestGracefulShutdownKeepsRunningDesired(t *testing.T) {
	dir := t.TempDir()

	// Graceful agent termination must not read as "the user stopped
	// these programs": the next life restores them.
	m1 := newManagerAt(t, dir)
	if _, err := m1.Install(shSpec("svc", "sleep 60", agentapi.RestartNever)); err != nil {
		t.Fatal(err)
	}

	if _, err := m1.Start("svc"); err != nil {
		t.Fatal(err)
	}

	m1.Shutdown()

	m2, err := NewManager(dir, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	defer m2.Shutdown()

	info := waitState(t, m2, "svc", agentapi.StateRunning)
	if info.PID == 0 {
		t.Fatalf("program not restored after graceful shutdown: %+v", info)
	}
}

func TestAutoStartStartsOnBoot(t *testing.T) {
	dir := t.TempDir()

	// An enabled program that was never started (desired stopped)
	// still comes up on the next agent boot, like an enabled systemd
	// unit at machine boot.
	m1 := newManagerAt(t, dir)
	spec := shSpec("enabled-svc", "sleep 60", agentapi.RestartNever)
	spec.AutoStart = true
	if _, err := m1.Install(spec); err != nil {
		t.Fatal(err)
	}

	m1.Shutdown()

	m2, err := NewManager(dir, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	defer m2.Shutdown()

	info := waitState(t, m2, "enabled-svc", agentapi.StateRunning)
	if info.PID == 0 {
		t.Fatalf("auto-start program not started on boot: %+v", info)
	}
}

func TestOneshotLifecycle(t *testing.T) {
	dir := t.TempDir()
	m1 := newManagerAt(t, dir)

	// Always is rejected for oneshot, like systemd Restart=always
	// with Type=oneshot.
	bad := shSpec("task-bad", "true", agentapi.RestartAlways)
	bad.Type = agentapi.TypeOneshot
	if _, err := m1.Install(bad); err == nil {
		t.Fatal("oneshot with Always restart policy not rejected")
	}

	spec := shSpec("task", "true", agentapi.RestartNever)
	spec.Type = agentapi.TypeOneshot
	if _, err := m1.Install(spec); err != nil {
		t.Fatal(err)
	}

	if _, err := m1.Start("task"); err != nil {
		t.Fatal(err)
	}

	// Successful completion is Exited, not Stopped or Failed.
	waitState(t, m1, "task", agentapi.StateExited)
	m1.Shutdown()

	// A completed oneshot without auto-start must not re-run on the
	// next boot.
	m2, err := NewManager(dir, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	defer m2.Shutdown()

	for _, info := range m2.List() {
		if info.Spec.Name == "task" && info.State == agentapi.StateRunning {
			t.Fatalf("completed oneshot re-ran on boot: %+v", info)
		}
	}
}

// makeArchive builds an in-memory tar.gz with the given files.
func makeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func TestInstallPackage(t *testing.T) {
	archive := makeArchive(t, map[string]string{
		"run.sh":     "#!/bin/sh\necho hi\n",
		"doc/README": "hello",
	})

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	m, err := NewManager(dir, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	ref := PackageRef{
		Name: "web", Version: "1.2.3", SHA256: digestOf(archive),
		URL: srv.URL, Entrypoint: "run.sh",
	}
	if err := m.InstallPackage(context.Background(), ref); err != nil {
		t.Fatal(err)
	}

	entry := filepath.Join(dir, "packages", "web", "1.2.3", "run.sh")
	st, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}

	if st.Mode().Perm()&0o111 == 0 {
		t.Error("entrypoint not executable")
	}

	// Same digest again: served from the local store, no download.
	if err := m.InstallPackage(context.Background(), ref); err != nil {
		t.Fatal(err)
	}

	if hits != 1 {
		t.Errorf("downloads = %d, want 1 (idempotent reinstall)", hits)
	}

	// Wrong digest must be rejected before anything is activated.
	bad := ref
	bad.Version = "2.0.0"
	bad.SHA256 = strings.Repeat("0", 64)
	if err := m.InstallPackage(context.Background(), bad); err == nil {
		t.Fatal("digest mismatch not rejected")
	}
}

func TestInstallPackageRejectsEscapes(t *testing.T) {
	archive := makeArchive(t, map[string]string{
		"../evil": "boom",
		"run.sh":  "#!/bin/sh\n",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	m, err := NewManager(t.TempDir(), testLogger())
	if err != nil {
		t.Fatal(err)
	}

	ref := PackageRef{
		Name: "evil", Version: "1.0.0", SHA256: digestOf(archive),
		URL: srv.URL, Entrypoint: "run.sh",
	}
	if err := m.InstallPackage(context.Background(), ref); err == nil {
		t.Fatal("escaping entry not rejected")
	}
}

func TestListPackages(t *testing.T) {
	dir := t.TempDir()
	m := newManagerAt(t, dir)

	t.Cleanup(m.Shutdown)

	// Two complete versions plus one incomplete (no digest marker,
	// e.g. an interrupted install) that must not be listed.
	for _, v := range []string{"1.0.0", "2.0.0"} {
		d := filepath.Join(dir, "packages", "web", v)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(d, ".dcnetlab-sha256"), []byte("digest-"+v), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.MkdirAll(filepath.Join(dir, "packages", "broken", "0.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	packages, err := m.ListPackages()
	if err != nil {
		t.Fatal(err)
	}

	if len(packages) != 2 {
		t.Fatalf("packages = %+v, want the 2 complete web versions", packages)
	}

	if packages[0].Version != "1.0.0" || packages[1].Version != "2.0.0" || packages[0].SHA256 != "digest-1.0.0" {
		t.Errorf("packages = %+v", packages)
	}
}

func TestRemovePackage(t *testing.T) {
	dir := t.TempDir()
	m := newManagerAt(t, dir)

	t.Cleanup(m.Shutdown)

	// A second version of the test package, plus a program pinning
	// the first one.
	pkgDir := filepath.Join(dir, "packages", testPackageName, "2.0.0")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(pkgDir, testEntrypoint), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Install(shSpec("pinner", "true", agentapi.RestartNever)); err != nil {
		t.Fatal(err)
	}

	// Removing all versions keeps the referenced one.
	removed, inUse, err := m.RemovePackage(testPackageName, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(removed) != 1 || removed[0] != "2.0.0" {
		t.Errorf("removed = %v, want [2.0.0]", removed)
	}

	if len(inUse) != 1 || inUse[0].Version != testPackageVersion {
		t.Errorf("inUse = %v, want [%s]", inUse, testPackageVersion)
	}

	if len(inUse) == 1 && (len(inUse[0].Programs) != 1 || inUse[0].Programs[0] != "pinner") {
		t.Errorf("inUse programs = %v, want [pinner]", inUse[0].Programs)
	}

	// Explicitly removing the referenced version is refused too.
	removed, inUse, err = m.RemovePackage(testPackageName, testPackageVersion)
	if err != nil {
		t.Fatal(err)
	}

	if len(removed) != 0 || len(inUse) != 1 {
		t.Errorf("removed = %v inUse = %v, want none removed", removed, inUse)
	}

	// After deleting the program the version goes away.
	if err := m.Remove("pinner"); err != nil {
		t.Fatal(err)
	}

	removed, _, err = m.RemovePackage(testPackageName, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(removed) != 1 || removed[0] != testPackageVersion {
		t.Errorf("removed = %v, want [%s]", removed, testPackageVersion)
	}

	if _, _, err := m.RemovePackage(testPackageName, ""); err == nil {
		t.Error("removing a gone package should fail")
	}
}
