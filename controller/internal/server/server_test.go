package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/biz"
	"github.com/ifantsai/dcnetlab/controller/internal/capture"
	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/controller/internal/data"
	"github.com/ifantsai/dcnetlab/controller/internal/metrics"
	"github.com/ifantsai/dcnetlab/controller/internal/observer"
	"github.com/ifantsai/dcnetlab/controller/internal/operation"
	"github.com/ifantsai/dcnetlab/controller/internal/service"
	"github.com/ifantsai/dcnetlab/controller/internal/traffic"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// Wire-format mirrors of the protobuf JSON mapping: int64 fields
// (generations) arrive as strings, list replies are wrapped.
type wireMeta struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Generation string `json:"generation"`
	Phase      string `json:"phase"`
}

type wireLab struct {
	Meta wireMeta `json:"meta"`
}

type wirePlan struct {
	ID             string `json:"id"`
	BaseGeneration string `json:"baseGeneration"`
	NewGeneration  string `json:"newGeneration"`
	State          string `json:"state"`
	Operations     []struct {
		Type    string `json:"type"`
		Target  string `json:"target"`
		Summary string `json:"summary"`
	} `json:"operations"`
	Allocations []struct {
		Pool  string `json:"pool"`
		Value string `json:"value"`
		Owner string `json:"owner"`
	} `json:"allocations"`
}

type wireOperation struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	dc := &conf.Data{Dir: dataDir}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	d, cleanup, err := data.NewData(dc, log)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(cleanup)
	ops := operation.NewManager(d, log, time.Minute)
	labs := biz.NewLabUsecase(d, ops, runtime.NoopDriver{}, dc, log)
	topos := biz.NewTopologyUsecase(d, log)
	sc := &conf.Server{HTTPAddr: "127.0.0.1:0", RepoAddr: "127.0.0.1:0"}
	packages, err := biz.NewPackageUsecase(d, dc, log)
	if err != nil {
		t.Fatal(err)
	}

	history := metrics.NewHistory(dc, log)
	t.Cleanup(history.Close)
	programs := biz.NewProgramUsecase(d, data.NewProgramAgent(runtime.NoopDriver{}, log), runtime.NoopDriver{}, packages, history, sc, log)
	plans := biz.NewPlanUsecase(d, ops, runtime.NoopDriver{}, programs, dc, log)
	opsUC := biz.NewOperationUsecase(d, log)
	power := biz.NewPowerUsecase(d, ops, runtime.NoopDriver{}, log)
	rt := biz.NewRuntimeUsecase(d, d, runtime.NoopDriver{}, log)
	trafficHistory := traffic.NewHistory()
	trafficUC := biz.NewTrafficUsecase(d, programs, trafficHistory, log)
	faultUC := biz.NewFaultUsecase(d, power, runtime.NoopDriver{}, log)
	captureMgr := capture.NewManager(runtime.NoopDriver{}, dataDir, log)
	captureUC, err := biz.NewCaptureUsecase(d, captureMgr, log)
	if err != nil {
		t.Fatal(err)
	}

	svc := service.NewDCNetLabService(labs, topos, plans, opsUC, power, rt, programs, packages, trafficUC, faultUC, captureUC, log)
	term := biz.NewTerminalUsecase(d, d, runtime.NoopDriver{}, log)
	obs := observer.New(d, runtime.NoopDriver{}, log)
	// The Kratos HTTP server implements http.Handler, so the full
	// transport stack (routing, codecs, error encoding) is under test.
	srv := httptest.NewServer(NewHTTPServer(sc, svc, term, obs, history, captureUC, SlogLogger{S: log}))
	t.Cleanup(srv.Close)

	return srv, dataDir
}

func doJSON[T any](t *testing.T, method, url string, body any, wantStatus int) T {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}

	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: decode: %v", method, url, err)
	}

	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d, want %d (%+v)", method, url, resp.StatusCode, wantStatus, out)
	}

	return out
}

func TestLabLifecycle(t *testing.T) {
	srv, dataDir := newTestServer(t)
	base := srv.URL + "/api/v1"

	// Create a micro lab.
	lab := doJSON[wireLab](t, "POST", base+"/labs",
		map[string]string{"name": "demo", "profile": "micro"}, http.StatusOK)
	if lab.Meta.Phase != "Pending" {
		t.Errorf("phase: %s", lab.Meta.Phase)
	}

	// Plan: micro profile = 9 nodes, 13 links.
	plan := doJSON[wirePlan](t, "POST", fmt.Sprintf("%s/labs/%s/plans", base, lab.Meta.ID), nil, http.StatusOK)
	if plan.NewGeneration != "1" {
		t.Errorf("new generation: %s", plan.NewGeneration)
	}

	var creates, linkOps int
	for _, op := range plan.Operations {
		switch op.Type {
		case "CreateNode":
			creates++
		case "CreateLink":
			linkOps++
		}
	}

	if creates != 9 || linkOps != 13 {
		t.Errorf("plan ops: %d nodes, %d links; want 9, 13", creates, linkOps)
	}

	if len(plan.Allocations) == 0 {
		t.Error("plan has no allocations")
	}

	// Nodes and links are persisted as desired state at plan time.
	nodes := doJSON[map[string][]json.RawMessage](t, "GET",
		fmt.Sprintf("%s/labs/%s/nodes", base, lab.Meta.ID), nil, http.StatusOK)
	if len(nodes["nodes"]) != 9 {
		t.Errorf("nodes: %d", len(nodes["nodes"]))
	}

	// Apply and wait for the operation to finish.
	acc := doJSON[map[string]string](t, "POST", fmt.Sprintf("%s/plans/%s/apply", base, plan.ID), nil, http.StatusOK)
	opID := acc["operationId"]
	var op wireOperation
	for deadline := time.Now().Add(10 * time.Second); ; {
		op = doJSON[wireOperation](t, "GET", base+"/operations/"+opID, nil, http.StatusOK)
		if op.State == "Succeeded" || op.State == "Failed" {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("operation timed out: %+v", op)
		}

		time.Sleep(20 * time.Millisecond)
	}

	if op.State != "Succeeded" {
		t.Fatalf("operation failed: %+v", op)
	}

	// Lab converged to generation 1 and Running.
	lab = doJSON[wireLab](t, "GET", base+"/labs/"+lab.Meta.ID, nil, http.StatusOK)
	if lab.Meta.Generation != "1" || lab.Meta.Phase != "Running" {
		t.Errorf("lab after apply: gen=%s phase=%s", lab.Meta.Generation, lab.Meta.Phase)
	}

	// Artifacts exist on disk.
	genDir := filepath.Join(dataDir, "labs", lab.Meta.ID, "generations", "1")
	if _, err := os.Stat(filepath.Join(genDir, "topology.clab.yml")); err != nil {
		t.Errorf("missing clab topology artifact: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(genDir, "configs", "*", "frr.conf"))
	if len(matches) != 9 { // 1 ext + 1 edge + 1 ss + 2 spine + 2 leaf + 2 server
		t.Errorf("frr configs: %d, want 9", len(matches))
	}

	// Generation snapshot is stored (int64 = string on the wire).
	gens := doJSON[map[string][]string](t, "GET",
		fmt.Sprintf("%s/labs/%s/generations", base, lab.Meta.ID), nil, http.StatusOK)
	if len(gens["generations"]) != 1 || gens["generations"][0] != "1" {
		t.Errorf("generations: %v", gens)
	}

	// Applying the same plan twice is rejected with a Kratos error.
	errBody := doJSON[map[string]any](t, "POST", fmt.Sprintf("%s/plans/%s/apply", base, plan.ID), nil, http.StatusBadRequest)
	if errBody["reason"] != "BAD_REQUEST" {
		t.Errorf("expected structured error, got %+v", errBody)
	}

	// Delete the lab.
	doJSON[map[string]string](t, "DELETE", base+"/labs/"+lab.Meta.ID, nil, http.StatusOK)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		resp, err := http.Get(base + "/labs/" + lab.Meta.ID)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Error("lab was not deleted")
}

func TestGetMissingLab(t *testing.T) {
	srv, _ := newTestServer(t)
	body := doJSON[map[string]any](t, "GET", srv.URL+"/api/v1/labs/nope", nil, http.StatusNotFound)
	if body["reason"] != "NOT_FOUND" {
		t.Errorf("got %+v", body)
	}
}

func TestCreateLabValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	base := srv.URL + "/api/v1"
	doJSON[map[string]any](t, "POST", base+"/labs", map[string]string{"profile": "micro"}, http.StatusBadRequest)
	doJSON[map[string]any](t, "POST", base+"/labs", map[string]string{"name": "x", "profile": "bogus"}, http.StatusBadRequest)
	// custom profile without topology
	doJSON[map[string]any](t, "POST", base+"/labs", map[string]string{"name": "y", "profile": "custom"}, http.StatusBadRequest)
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	body := doJSON[map[string]string](t, "GET", srv.URL+"/api/v1/healthz", nil, http.StatusOK)
	if body["status"] != "ok" {
		t.Errorf("healthz: %+v", body)
	}
}
