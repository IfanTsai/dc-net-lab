package capture

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"

	"github.com/ifantsai/dcnetlab/internal/agentapi"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

func TestWindowEviction(t *testing.T) {
	w := newWindow(3)
	for i := range 5 {
		w.append(Packet{Row: PacketRow{Index: int64(i)}})
	}

	if w.total != 5 || w.first() != 2 {
		t.Fatalf("total=%d first=%d", w.total, w.first())
	}

	if _, ok := w.get(1); ok {
		t.Error("evicted packet still readable")
	}

	p, ok := w.get(4)
	if !ok || p.Row.Index != 4 {
		t.Errorf("get(4): %+v ok=%v", p, ok)
	}

	page := w.page(0, 10)
	if len(page) != 3 || page[0].Row.Index != 2 {
		t.Errorf("page: %+v", page)
	}
}

func TestArgs(t *testing.T) {
	spec := model.CaptureSessionSpec{
		Interface:  "eth1",
		Direction:  model.CaptureDirectionRx,
		SnapLength: 256,
		Duration:   30 * time.Second,
		MaxBytes:   100 << 20,
		Filter: model.CaptureFilter{
			Protocol:  model.CaptureProtocolBGP,
			SrcPrefix: "10.0.0.0/16",
			Port:      179,
		},
	}

	args := Args(spec)

	// The tool path must come first: docker exec runs argv verbatim,
	// and a missing binary path is exactly the class of bug unit tests
	// on flag content alone would miss.
	if args[0] != agentapi.CaptureMount {
		t.Fatalf("argv[0] = %s, want %s", args[0], agentapi.CaptureMount)
	}

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--iface eth1", "--snap 256", "--duration 30s", "--direction rx",
		"--max-bytes 104857600", "--proto bgp", "--src 10.0.0.0/16", "--port 179",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}

	if strings.Contains(joined, "--max-packets") || strings.Contains(joined, "--dst") {
		t.Errorf("zero-valued flags emitted: %s", joined)
	}
}

// fakeStream feeds a canned pcapng stream and records Close calls.
type fakeStream struct {
	r       io.Reader
	onClose func()

	mu     sync.Mutex
	closed bool
}

func (s *fakeStream) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *fakeStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.closed && s.onClose != nil {
		s.onClose()
	}

	s.closed = true

	return nil
}

type fakeDriver struct {
	runtime.NoopDriver

	stream *fakeStream
}

func (d *fakeDriver) ExecStream(_ context.Context, _, _ string, _ []string) (runtime.ExecSession, error) {
	return d.stream, nil
}

// pcapngStream builds a two-packet pcapng byte stream.
func pcapngStream(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	w, err := pcapgo.NewNgWriterInterface(&buf, pcapgo.NgInterface{
		Name: "eth1", LinkType: layers.LinkTypeEthernet, SnapLength: 256,
	}, pcapgo.DefaultNgWriterOptions)
	if err != nil {
		t.Fatal(err)
	}

	for i, dir := range []pcapgo.NgEpbFlag{pcapgo.NgEpbFlagDirectionInbound, pcapgo.NgEpbFlagDirectionOutbound} {
		frame := tcpFrame(t, uint16(33000+i), 8080, []byte("hi"))
		flags := pcapgo.NgEpbFlags{Direction: dir}
		if err := w.WritePacketWithOptions(gopacket.CaptureInfo{
			Timestamp: time.Now(), CaptureLength: len(frame), Length: len(frame),
		}, frame, pcapgo.NgPacketOptions{Flags: &flags}); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func testSession() *model.CaptureSession {
	return &model.CaptureSession{
		Meta: model.ResourceMeta{ID: "cap-1", Name: "t"},
		Spec: model.CaptureSessionSpec{
			LabID: "lab-1", NodeName: "leaf-a", Interface: "eth1",
			Direction: model.CaptureDirectionBoth, SnapLength: 256, Duration: time.Second,
		},
	}
}

func TestManagerSessionLifecycle(t *testing.T) {
	dir := t.TempDir()
	raw := pcapngStream(t)
	driver := &fakeDriver{stream: &fakeStream{r: bytes.NewReader(raw)}}
	m := NewManager(driver, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))

	done := make(chan Result, 1)
	if err := m.StartSession("lab", testSession(), func(res Result) { done <- res }); err != nil {
		t.Fatal(err)
	}

	res := <-done
	if res.Err != nil || res.Packets != 2 || res.UserStopped {
		t.Fatalf("result: %+v", res)
	}

	// The recording file holds the raw stream verbatim.
	data, err := os.ReadFile(m.FilePath("lab-1", "cap-1"))
	if err != nil || !bytes.Equal(data, raw) {
		t.Fatalf("recording mismatch: len=%d err=%v", len(data), err)
	}

	// The live window survives session end for the viewer.
	rows, total, first, ok := m.Page("cap-1", 0, 10)
	if !ok || total != 2 || first != 0 || len(rows) != 2 {
		t.Fatalf("page: ok=%v total=%d first=%d rows=%d", ok, total, first, len(rows))
	}

	if rows[0].Direction != model.CaptureDirectionRx || rows[1].Direction != model.CaptureDirectionTx {
		t.Errorf("directions: %s, %s", rows[0].Direction, rows[1].Direction)
	}

	if rows[0].Protocol != "TCP" || rows[0].Index != 0 || rows[1].Index != 1 {
		t.Errorf("rows: %+v", rows)
	}

	// Single-packet access decodes.
	p, ok := m.Packet("cap-1", 1)
	if !ok || p.Row.Source != "10.0.0.1" {
		t.Errorf("packet(1): %+v ok=%v", p.Row, ok)
	}

	// ReadPage over the finished file agrees with the live window.
	fileRows, fileTotal, err := ReadPage(m.FilePath("lab-1", "cap-1"), 0, 10)
	if err != nil || fileTotal != 2 || len(fileRows) != 2 {
		t.Fatalf("file page: total=%d rows=%d err=%v", fileTotal, len(fileRows), err)
	}

	if fileRows[1].Direction != model.CaptureDirectionTx {
		t.Errorf("file row direction: %s", fileRows[1].Direction)
	}
}

// blockingReader blocks until closed, simulating a silent interface.
type blockingReader struct {
	unblock chan struct{}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	<-r.unblock

	return 0, io.EOF
}

func TestManagerUserStop(t *testing.T) {
	// A header-only stream (no packets yet) followed by a silent
	// interface: reads block until the session is closed.
	var header bytes.Buffer
	w, err := pcapgo.NewNgWriterInterface(&header, pcapgo.NgInterface{
		Name: "eth1", LinkType: layers.LinkTypeEthernet, SnapLength: 256,
	}, pcapgo.DefaultNgWriterOptions)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	unblock := make(chan struct{})
	stream := &fakeStream{r: io.MultiReader(bytes.NewReader(header.Bytes()), &blockingReader{unblock: unblock})}
	stream.onClose = func() { close(unblock) }

	driver := &fakeDriver{stream: stream}
	m := NewManager(driver, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	done := make(chan Result, 1)
	if err := m.StartSession("lab", testSession(), func(res Result) { done <- res }); err != nil {
		t.Fatal(err)
	}

	m.StopSession("cap-1")

	res := <-done
	if !res.UserStopped {
		t.Fatalf("result: %+v", res)
	}
}

func TestSubscribeSnapshotThenBatch(t *testing.T) {
	dir := t.TempDir()
	driver := &fakeDriver{stream: &fakeStream{r: bytes.NewReader(pcapngStream(t))}}
	m := NewManager(driver, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))

	done := make(chan Result, 1)
	if err := m.StartSession("lab", testSession(), func(res Result) { done <- res }); err != nil {
		t.Fatal(err)
	}

	<-done

	snapshot, events, cancel, ok := m.Subscribe("cap-1")
	if !ok {
		t.Fatal("no live state")
	}

	defer cancel()

	if snapshot.State == "" || len(snapshot.Packets) != 2 {
		t.Fatalf("snapshot: state=%q packets=%d", snapshot.State, len(snapshot.Packets))
	}

	indices := []int64{snapshot.Packets[0].Index, snapshot.Packets[1].Index}
	if !slices.Equal(indices, []int64{0, 1}) {
		t.Errorf("indices: %v", indices)
	}

	if _, open := <-events; open {
		t.Error("finished session should have a closed event channel")
	}
}
