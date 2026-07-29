package biz

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"

	"github.com/ifantsai/dcnetlab/internal/capture"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// fakeCaptureRepo keeps everything in memory.
type fakeCaptureRepo struct {
	lab      *model.Lab
	nodes    []*model.Node
	links    []*model.Link
	sessions map[string]*model.CaptureSession
}

func (r *fakeCaptureRepo) GetLab(id string) (*model.Lab, error) {
	if r.lab == nil || r.lab.Meta.ID != id {
		return nil, ErrNotFound
	}

	return r.lab, nil
}

func (r *fakeCaptureRepo) ListNodes(string) ([]*model.Node, error) { return r.nodes, nil }
func (r *fakeCaptureRepo) ListLinks(string) ([]*model.Link, error) { return r.links, nil }

func (r *fakeCaptureRepo) CreateCaptureSession(c *model.CaptureSession) error {
	if r.sessions == nil {
		r.sessions = map[string]*model.CaptureSession{}
	}

	cp := *c
	r.sessions[c.Meta.ID] = &cp

	return nil
}

func (r *fakeCaptureRepo) UpdateCaptureSession(c *model.CaptureSession) error {
	if _, ok := r.sessions[c.Meta.ID]; !ok {
		return ErrNotFound
	}

	cp := *c
	r.sessions[c.Meta.ID] = &cp

	return nil
}

func (r *fakeCaptureRepo) DeleteCaptureSession(id string) error {
	if _, ok := r.sessions[id]; !ok {
		return ErrNotFound
	}

	delete(r.sessions, id)

	return nil
}

func (r *fakeCaptureRepo) GetCaptureSession(id string) (*model.CaptureSession, error) {
	s, ok := r.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}

	cp := *s

	return &cp, nil
}

func (r *fakeCaptureRepo) ListCaptureSessions(labID string) ([]*model.CaptureSession, error) {
	var out []*model.CaptureSession
	for _, s := range r.sessions {
		if s.Spec.LabID == labID {
			cp := *s
			out = append(out, &cp)
		}
	}

	return out, nil
}

func (r *fakeCaptureRepo) ListAllCaptureSessions() ([]*model.CaptureSession, error) {
	var out []*model.CaptureSession
	for _, s := range r.sessions {
		cp := *s
		out = append(out, &cp)
	}

	return out, nil
}

// fakeCaptureDriver serves each ExecStream from a canned pcapng
// stream and records the argv it was given.
type fakeCaptureDriver struct {
	runtime.NoopDriver

	payload []byte
	argv    []string
}

type memStream struct{ r io.Reader }

func (s *memStream) Read(p []byte) (int, error) { return s.r.Read(p) }
func (s *memStream) Close() error               { return nil }

func (d *fakeCaptureDriver) ExecStream(_ context.Context, _, _ string, cmd []string) (runtime.ExecSession, error) {
	d.argv = cmd

	return &memStream{r: bytes.NewReader(d.payload)}, nil
}

func captureTestFrame(t *testing.T) []byte {
	t.Helper()

	buf := gopacket.NewSerializeBuffer()
	ip := &layers.IPv4{
		Version: 4, TTL: 64, Protocol: layers.IPProtocolICMPv4,
		SrcIP: []byte{10, 100, 0, 11}, DstIP: []byte{10, 100, 1, 11},
	}

	err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		&layers.Ethernet{
			SrcMAC: []byte{2, 0, 0, 0, 0, 1}, DstMAC: []byte{2, 0, 0, 0, 0, 2},
			EthernetType: layers.EthernetTypeIPv4,
		},
		ip,
		&layers.ICMPv4{TypeCode: layers.CreateICMPv4TypeCode(layers.ICMPv4TypeEchoRequest, 0)})
	if err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func capturePayload(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	w, err := pcapgo.NewNgWriterInterface(&buf, pcapgo.NgInterface{
		Name: "eth1", LinkType: layers.LinkTypeEthernet, SnapLength: 256,
	}, pcapgo.DefaultNgWriterOptions)
	if err != nil {
		t.Fatal(err)
	}

	frame := captureTestFrame(t)
	if err := w.WritePacket(gopacket.CaptureInfo{
		Timestamp: time.Now(), CaptureLength: len(frame), Length: len(frame),
	}, frame); err != nil {
		t.Fatal(err)
	}

	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func newCaptureFixture(t *testing.T) (*CaptureUsecase, *fakeCaptureRepo, *fakeCaptureDriver) {
	t.Helper()

	repo := &fakeCaptureRepo{
		sessions: map[string]*model.CaptureSession{},
		lab:      &model.Lab{Meta: model.ResourceMeta{ID: "lab-1", Name: "dc1", Generation: 1}},
		nodes: []*model.Node{
			{
				Meta: model.ResourceMeta{ID: "node-leaf", Name: "leaf-a"},
				Spec: model.NodeSpec{LabID: "lab-1", Role: model.RoleLeaf, VlanID: 1000},
			},
			{
				Meta: model.ResourceMeta{ID: "node-server", Name: "server-1"},
				Spec: model.NodeSpec{
					LabID: "lab-1", Role: model.RoleServer,
					Address: netip.MustParsePrefix("10.100.0.11/24"),
				},
			},
		},
		links: []*model.Link{{
			Meta: model.ResourceMeta{ID: "link-1", Name: "leaf-a--server-1"},
			Spec: model.LinkSpec{
				LabID:     "lab-1",
				EndpointA: model.LinkEndpoint{NodeID: "node-leaf", NodeName: "leaf-a", Interface: "eth3"},
				EndpointB: model.LinkEndpoint{NodeID: "node-server", NodeName: "server-1", Interface: "eth1"},
			},
		}},
	}

	driver := &fakeCaptureDriver{payload: capturePayload(t)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := capture.NewManager(driver, t.TempDir(), log)

	uc, err := NewCaptureUsecase(repo, mgr, log)
	if err != nil {
		t.Fatal(err)
	}

	return uc, repo, driver
}

// waitState polls until the session reaches a terminal state (the
// pipeline finishes asynchronously).
func waitState(t *testing.T, uc *CaptureUsecase, id string, want string) *model.CaptureSession {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		sess, err := uc.GetCaptureSession("lab-1", id)
		if err != nil {
			t.Fatal(err)
		}

		if sess.Status.State == want {
			return sess
		}

		if time.Now().After(deadline) {
			t.Fatalf("session state %s, want %s", sess.Status.State, want)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestCreateCaptureSessionRunsToCompletion(t *testing.T) {
	uc, _, driver := newCaptureFixture(t)

	sess, err := uc.CreateCaptureSession(context.Background(), "lab-1", "demo", model.CaptureSessionSpec{
		NodeID: "node-server", Interface: "eth1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Defaults were applied and the argv carries the mounted tool path
	// first (the wire contract with the in-container binary).
	if sess.Spec.SnapLength != 256 || sess.Spec.Duration != 30*time.Second || sess.Spec.Direction != model.CaptureDirectionBoth {
		t.Errorf("defaults: %+v", sess.Spec)
	}

	if len(driver.argv) == 0 || driver.argv[0] != "/opt/dcnetlab/bin/capture" {
		t.Errorf("argv: %v", driver.argv)
	}

	done := waitState(t, uc, sess.Meta.ID, model.CaptureStateCompleted)
	if done.Status.Packets != 1 || done.Status.LastError != "" {
		t.Errorf("status: %+v", done.Status)
	}

	// The decoded packet is readable through the usecase.
	rows, total, _, err := uc.CapturePackets("lab-1", sess.Meta.ID, 0, 10)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Protocol != "ICMP" {
		t.Errorf("packets: total=%d rows=%+v err=%v", total, rows, err)
	}

	row, layers, data, err := uc.CapturePacket("lab-1", sess.Meta.ID, 0)
	if err != nil || row.Source != "10.100.0.11" || len(layers) == 0 || len(data) == 0 {
		t.Errorf("packet detail: %+v layers=%d err=%v", row, len(layers), err)
	}
}

func TestCreateCaptureSessionValidation(t *testing.T) {
	uc, _, _ := newCaptureFixture(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		reqName string
		spec    model.CaptureSessionSpec
		wantErr string
	}{
		{"bad name", "Bad_Name", model.CaptureSessionSpec{NodeID: "node-server", Interface: "eth1"}, "invalid session name"},
		{"unknown node", "a", model.CaptureSessionSpec{NodeID: "nope", Interface: "eth1"}, "not found"},
		{"management interface", "b", model.CaptureSessionSpec{NodeID: "node-server", Interface: "eth0"}, "not a modelled interface"},
		{"implementation bridge", "c", model.CaptureSessionSpec{NodeID: "node-leaf", Interface: "br0"}, "not a modelled interface"},
		{"bad direction", "d", model.CaptureSessionSpec{NodeID: "node-server", Interface: "eth1", Direction: "in"}, "invalid direction"},
		{"long duration", "e", model.CaptureSessionSpec{NodeID: "node-server", Interface: "eth1", Duration: time.Hour}, "duration"},
		{"bad filter proto", "f", model.CaptureSessionSpec{NodeID: "node-server", Interface: "eth1", Filter: model.CaptureFilter{Protocol: "gre"}}, "protocol"},
		{"bad filter prefix", "g", model.CaptureSessionSpec{NodeID: "node-server", Interface: "eth1", Filter: model.CaptureFilter{SrcPrefix: "nope"}}, "srcPrefix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := uc.CreateCaptureSession(ctx, "lab-1", tt.reqName, tt.spec)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestCaptureLogicalInterfaces(t *testing.T) {
	uc, _, _ := newCaptureFixture(t)
	ctx := context.Background()

	// The leaf's vlanif and the server's bond0 are modelled logical
	// interfaces, valid capture targets alongside link endpoints.
	if _, err := uc.CreateCaptureSession(ctx, "lab-1", "vlanif", model.CaptureSessionSpec{
		NodeID: "node-leaf", Interface: "vlan1000",
	}); err != nil {
		t.Errorf("vlanif rejected: %v", err)
	}

	if _, err := uc.CreateCaptureSession(ctx, "lab-1", "bond", model.CaptureSessionSpec{
		NodeID: "node-server", Interface: "bond0",
	}); err != nil {
		t.Errorf("bond0 rejected: %v", err)
	}
}

func TestCaptureUndeployedLabRejected(t *testing.T) {
	uc, repo, _ := newCaptureFixture(t)
	repo.lab.Meta.Generation = 0

	_, err := uc.CreateCaptureSession(context.Background(), "lab-1", "demo", model.CaptureSessionSpec{
		NodeID: "node-server", Interface: "eth1",
	})
	if err == nil || !strings.Contains(err.Error(), "no deployed generation") {
		t.Errorf("error = %v", err)
	}
}

func TestCapturePerNodeBudget(t *testing.T) {
	uc, repo, _ := newCaptureFixture(t)

	// Two sessions already Running on the node exhaust the budget.
	for _, id := range []string{"cap-a", "cap-b"} {
		repo.sessions[id] = &model.CaptureSession{
			Meta:   model.ResourceMeta{ID: id, Name: id},
			Spec:   model.CaptureSessionSpec{LabID: "lab-1", NodeID: "node-server"},
			Status: model.CaptureSessionStatus{State: model.CaptureStateRunning},
		}
	}

	_, err := uc.CreateCaptureSession(context.Background(), "lab-1", "third", model.CaptureSessionSpec{
		NodeID: "node-server", Interface: "eth1",
	})
	if err == nil || !strings.Contains(err.Error(), "running captures") {
		t.Errorf("error = %v", err)
	}
}

func TestCaptureInterruptedSessionsFailedAtStartup(t *testing.T) {
	repo := &fakeCaptureRepo{sessions: map[string]*model.CaptureSession{
		"cap-x": {
			Meta:   model.ResourceMeta{ID: "cap-x", Name: "x"},
			Spec:   model.CaptureSessionSpec{LabID: "lab-1"},
			Status: model.CaptureSessionStatus{State: model.CaptureStateRunning},
		},
	}}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := capture.NewManager(runtime.NoopDriver{}, t.TempDir(), log)
	if _, err := NewCaptureUsecase(repo, mgr, log); err != nil {
		t.Fatal(err)
	}

	sess := repo.sessions["cap-x"]
	if sess.Status.State != model.CaptureStateFailed || !strings.Contains(sess.Status.LastError, "restarted") {
		t.Errorf("status: %+v", sess.Status)
	}
}

func TestCaptureStopFinishedIsNoop(t *testing.T) {
	uc, _, _ := newCaptureFixture(t)

	sess, err := uc.CreateCaptureSession(context.Background(), "lab-1", "demo", model.CaptureSessionSpec{
		NodeID: "node-server", Interface: "eth1",
	})
	if err != nil {
		t.Fatal(err)
	}

	waitState(t, uc, sess.Meta.ID, model.CaptureStateCompleted)

	stopped, err := uc.StopCaptureSession(context.Background(), "lab-1", sess.Meta.ID)
	if err != nil || stopped.Status.State != model.CaptureStateCompleted {
		t.Errorf("stop after completion: state=%s err=%v", stopped.Status.State, err)
	}
}

func TestCaptureDeleteRemovesRecording(t *testing.T) {
	uc, repo, _ := newCaptureFixture(t)

	sess, err := uc.CreateCaptureSession(context.Background(), "lab-1", "demo", model.CaptureSessionSpec{
		NodeID: "node-server", Interface: "eth1",
	})
	if err != nil {
		t.Fatal(err)
	}

	waitState(t, uc, sess.Meta.ID, model.CaptureStateCompleted)

	path, _, err := uc.RecordingPath("lab-1", sess.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := uc.DeleteCaptureSession(context.Background(), "lab-1", sess.Meta.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("recording still on disk: %v", err)
	}

	if _, ok := repo.sessions[sess.Meta.ID]; ok {
		t.Error("session row still present")
	}
}
