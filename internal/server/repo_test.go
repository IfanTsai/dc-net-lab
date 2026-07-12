package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/data"
)

// packageArchive builds a minimal valid package tar.gz.
func packageArchive(t *testing.T, name, version string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"manifest.json": fmt.Sprintf(`{"name":%q,"version":%q,"entrypoint":"run.sh"}`, name, version),
		"run.sh":        "#!/bin/sh\n",
	}

	for fname, content := range files {
		hdr := &tar.Header{Name: fname, Mode: 0o644, Size: int64(len(content))}
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

func TestRepoServer(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dc := &conf.Data{Dir: t.TempDir()}
	d, cleanup, err := data.NewData(dc, log)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(cleanup)

	packages, err := biz.NewPackageUsecase(d, dc, log)
	if err != nil {
		t.Fatal(err)
	}

	archive := packageArchive(t, "demo", "1.0.0")
	if _, err := packages.UploadPackage(archive); err != nil {
		t.Fatal(err)
	}

	if _, err := packages.UploadPackage(packageArchive(t, "web", "2.0.0")); err != nil {
		t.Fatal(err)
	}

	rs := NewRepoServer(&conf.Server{RepoAddr: "127.0.0.1:0"}, packages, log)
	srv := httptest.NewServer(rs.handler())

	t.Cleanup(srv.Close)

	fetchIndex := func(path string, wantStatus int) []repoIndexEntry {
		t.Helper()

		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != wantStatus {
			t.Fatalf("GET %s: status %d, want %d", path, resp.StatusCode, wantStatus)
		}

		if wantStatus != http.StatusOK {
			return nil
		}

		var entries []repoIndexEntry
		if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
			t.Fatal(err)
		}

		return entries
	}

	if entries := fetchIndex("/packages", http.StatusOK); len(entries) != 2 {
		t.Errorf("index size = %d, want 2", len(entries))
	}

	entries := fetchIndex("/packages/demo", http.StatusOK)
	if len(entries) != 1 || entries[0].Version != "1.0.0" || entries[0].Entrypoint != "run.sh" {
		t.Errorf("filtered index = %+v", entries)
	}

	fetchIndex("/packages/nosuch", http.StatusNotFound)

	// The archive download returns the stored payload verbatim.
	resp, err := http.Get(srv.URL + "/packages/demo/1.0.0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, archive) {
		t.Errorf("download status %d, %d bytes, want the %d byte upload", resp.StatusCode, len(body), len(archive))
	}
}
