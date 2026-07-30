package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/biz"
	"github.com/ifantsai/dcnetlab/controller/internal/conf"
)

// repoShutdownTimeout bounds the graceful shutdown of the package
// repository server.
const repoShutdownTimeout = 3 * time.Second

// RepoServer serves the package repository over plain HTTP for the
// node-agents on the management network: GET
// /packages/<name>/<version> streams the tar.gz archive. It is
// read-only and carries no other surface; uploads go through the
// main API. It implements kratos transport.Server so its lifecycle
// follows the application.
type RepoServer struct {
	srv      *http.Server
	packages *biz.PackageUsecase
	log      *slog.Logger
}

// NewRepoServer wires the package repository listener; an empty
// RepoAddr disables it.
func NewRepoServer(c *conf.Server, packages *biz.PackageUsecase, log *slog.Logger) *RepoServer {
	s := &RepoServer{packages: packages, log: log}
	if c.RepoAddr == "" {
		return s
	}

	s.srv = &http.Server{Addr: c.RepoAddr, Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}

	return s
}

// handler builds the repository routes: the index, a per-package
// index and the archive download.
func (s *RepoServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /packages", s.serveIndex)
	mux.HandleFunc("GET /packages/", s.servePackage)

	return mux
}

// Start runs the repository listener until Stop.
func (s *RepoServer) Start(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}

	s.log.Info("package repository listening", "addr", s.srv.Addr)
	if err := s.srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// Stop shuts the repository listener down.
func (s *RepoServer) Stop(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, repoShutdownTimeout)
	defer cancel()

	return s.srv.Shutdown(shutdownCtx)
}

// repoIndexEntry is one package version in the JSON index consumed
// by the node-agent pkg CLI.
type repoIndexEntry struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Entrypoint  string `json:"entrypoint"`
	Description string `json:"description,omitempty"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"sizeBytes"`
	Builtin     bool   `json:"builtin,omitempty"`
}

// servePackage handles everything under /packages/: a bare name
// serves that package's index slice, name/version streams the
// archive.
func (s *RepoServer) servePackage(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/packages/")
	name, version, ok := strings.Cut(rest, "/")
	if name == "" || strings.Contains(version, "/") {
		http.NotFound(w, r)

		return
	}

	if !ok || version == "" {
		s.writeIndex(w, r, name)

		return
	}

	path, err := s.packages.PackagePayloadPath(name, version)
	if err != nil {
		http.NotFound(w, r)

		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, path)
}

// serveIndex lists every package: GET /packages.
func (s *RepoServer) serveIndex(w http.ResponseWriter, r *http.Request) {
	s.writeIndex(w, r, "")
}

// writeIndex writes the JSON index, optionally filtered to one
// package name (404 when that name is unknown).
func (s *RepoServer) writeIndex(w http.ResponseWriter, r *http.Request, filter string) {
	packages, err := s.packages.ListPackages()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	entries := make([]repoIndexEntry, 0, len(packages))
	for _, p := range packages {
		if filter != "" && p.Meta.Name != filter {
			continue
		}

		entries = append(entries, repoIndexEntry{
			Name:        p.Meta.Name,
			Version:     p.Spec.Version,
			Entrypoint:  p.Spec.Entrypoint,
			Description: p.Spec.Description,
			SHA256:      p.Status.SHA256,
			SizeBytes:   p.Status.SizeBytes,
			Builtin:     p.Status.Builtin,
		})
	}

	if filter != "" && len(entries) == 0 {
		http.NotFound(w, r)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}
