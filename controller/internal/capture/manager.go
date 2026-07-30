package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gopacket/gopacket/pcapgo"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

const (
	// windowSize bounds the live in-memory packet window per session:
	// with the default 256-byte snap that is a few MB.
	windowSize = 10000
	// pushInterval batches live packet rows towards WS subscribers.
	pushInterval = 200 * time.Millisecond
	// subscriberBuffer is each subscriber's event queue; a full queue
	// drops the batch for that subscriber, which the UI detects as an
	// index gap.
	subscriberBuffer = 32
	// retention is how long a finished recording stays on disk.
	retention = 24 * time.Hour
	// gcInterval is how often expired recordings are swept.
	gcInterval = time.Hour
)

// Event is one push on a capture subscription: freshly decoded rows
// plus the window bounds, and the final state once the session ends.
type Event struct {
	Type           string      `json:"type"` // "packets" or "end"
	Packets        []PacketRow `json:"packets,omitempty"`
	Total          int64       `json:"total"`
	FirstAvailable int64       `json:"firstAvailable"`
	State          string      `json:"state,omitempty"`
}

// Result is what a finished capture reports back to the usecase.
type Result struct {
	Packets     int64
	Bytes       int64
	UserStopped bool
	Err         error
}

// Manager runs capture sessions: it execs the in-container capture
// tool through the runtime driver, tees the pcapng stream to disk,
// decodes packets into a live window and fans batches out to
// subscribers. Finished sessions keep their window in memory until
// deleted (or a controller restart), so the viewer does not have to
// re-parse the file.
type Manager struct {
	driver  runtime.Driver
	dataDir string
	log     *slog.Logger

	mu       sync.Mutex
	sessions map[string]*liveSession

	gcCancel context.CancelFunc
	gcDone   chan struct{}
}

type liveSession struct {
	id    string
	state string // "" while running, else the final state

	mu      sync.Mutex
	window  *window
	bytes   int64
	pending []PacketRow
	subs    map[chan Event]bool
	stopped bool // user asked to stop

	stream runtime.ExecSession
	done   chan struct{}
}

// NewManager wires the capture manager.
func NewManager(driver runtime.Driver, dataDir string, log *slog.Logger) *Manager {
	return &Manager{
		driver:   driver,
		dataDir:  dataDir,
		log:      log,
		sessions: make(map[string]*liveSession),
	}
}

// FilePath returns where a session's pcapng recording lives.
func (m *Manager) FilePath(labID, sessionID string) string {
	return filepath.Join(m.dataDir, "labs", labID, "captures", sessionID+".pcapng")
}

// Args builds the in-container capture command line from a spec. The
// first element is the tool's in-container path (baked into switch
// images, package-installed on servers) — the whole command is the
// wire contract with nodeapps/cmd/capture.
func Args(spec model.CaptureSessionSpec) []string {
	args := []string{
		nodeagentapi.CapturePath,
		"--iface", spec.Interface,
		"--snap", strconv.Itoa(spec.SnapLength),
		"--duration", spec.Duration.String(),
		"--direction", spec.Direction,
	}

	if spec.MaxPackets > 0 {
		args = append(args, "--max-packets", strconv.FormatUint(spec.MaxPackets, 10))
	}

	if spec.MaxBytes > 0 {
		args = append(args, "--max-bytes", strconv.FormatUint(spec.MaxBytes, 10))
	}

	f := spec.Filter
	if f.Protocol != "" {
		args = append(args, "--proto", f.Protocol)
	}

	if f.SrcPrefix != "" {
		args = append(args, "--src", f.SrcPrefix)
	}

	if f.DstPrefix != "" {
		args = append(args, "--dst", f.DstPrefix)
	}

	if f.Port != 0 {
		args = append(args, "--port", strconv.Itoa(f.Port))
	}

	return args
}

// Start launches the capture and returns once the exec stream is up;
// decoding runs in the background until the stream ends, then onEnd
// reports the outcome (from the reader goroutine).
func (m *Manager) StartSession(labName string, sess *model.CaptureSession, onEnd func(Result)) error {
	path := m.FilePath(sess.Spec.LabID, sess.Meta.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create capture directory: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create capture file: %w", err)
	}

	// The session must outlive the request context; its lifetime is
	// owned by Stop/Drop via the exec stream's stdin.
	stream, err := m.driver.ExecStream(context.Background(), labName, sess.Spec.NodeName, Args(sess.Spec))
	if err != nil {
		_ = file.Close()

		return fmt.Errorf("start capture: %w", err)
	}

	ls := &liveSession{
		id:     sess.Meta.ID,
		window: newWindow(windowSize),
		subs:   make(map[chan Event]bool),
		stream: stream,
		done:   make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[sess.Meta.ID] = ls
	m.mu.Unlock()

	go m.run(ls, stream, file, onEnd)
	go ls.pushLoop()

	return nil
}

// run consumes the pcapng stream until it ends, teeing the raw bytes
// to the recording file and decoding rows into the live window.
func (m *Manager) run(ls *liveSession, stream runtime.ExecSession, file *os.File, onEnd func(Result)) {
	err := m.consume(ls, stream, file)

	closeErr := stream.Close()
	if err == nil && !ls.userStopped() {
		// The tool exiting by itself is normal (duration/limit); an
		// abnormal exec exit (bad interface, missing binary) surfaces
		// here through stderr.
		err = closeErr
	}

	if syncErr := file.Sync(); syncErr != nil && err == nil {
		err = syncErr
	}

	_ = file.Close()

	ls.mu.Lock()
	packets, bytes, stopped := ls.window.total, ls.bytes, ls.stopped
	ls.mu.Unlock()

	res := Result{Packets: packets, Bytes: bytes, UserStopped: stopped}
	if !stopped {
		res.Err = err
	}

	state := model.CaptureStateCompleted
	switch {
	case stopped:
		state = model.CaptureStateStopped
	case res.Err != nil:
		state = model.CaptureStateFailed
	}

	// onEnd persists the final status before done is closed, so a
	// caller blocked in StopSession returns to an already-consistent
	// database; subscribers hear about the end last.
	onEnd(res)
	ls.finish(state)
	close(ls.done)
}

func (m *Manager) consume(ls *liveSession, stream io.Reader, file io.Writer) error {
	r, err := pcapgo.NewNgReader(io.TeeReader(stream, file), pcapgo.DefaultNgReaderOptions)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil // stream ended before the first block: tool failed at startup
		}

		return fmt.Errorf("read pcapng header: %w", err)
	}

	for {
		data, ci, opts, err := r.ReadPacketDataWithOptions()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}

			return fmt.Errorf("read pcapng stream: %w", err)
		}

		row := PacketRow{
			Ts:            ci.Timestamp,
			Direction:     direction(opts),
			CaptureLength: ci.CaptureLength,
			WireLength:    ci.Length,
			Summary:       Summarize(data),
		}

		ls.append(Packet{Row: row, Data: data}, int64(ci.CaptureLength))
	}
}

// direction maps the pcapng EPB flags word back onto rx/tx.
func direction(opts pcapgo.NgPacketOptions) string {
	if opts.Flags == nil {
		return ""
	}

	switch opts.Flags.Direction {
	case pcapgo.NgEpbFlagDirectionInbound:
		return model.CaptureDirectionRx
	case pcapgo.NgEpbFlagDirectionOutbound:
		return model.CaptureDirectionTx
	default:
		return ""
	}
}

// Stop ends a running session on the user's behalf; the reader
// goroutine reports the final counters through onEnd. Stopping a
// session that already ended (or was never live) is a no-op.
func (m *Manager) StopSession(id string) {
	m.mu.Lock()
	ls := m.sessions[id]
	m.mu.Unlock()

	if ls == nil {
		return
	}

	ls.mu.Lock()
	alreadyDone := ls.state != ""
	ls.stopped = true
	ls.mu.Unlock()

	if alreadyDone {
		return
	}

	_ = ls.stream.Close()
	<-ls.done
}

// Drop stops the session if needed and forgets its live state (on
// delete). The recording file is the usecase's to remove.
func (m *Manager) Drop(id string) {
	m.StopSession(id)

	m.mu.Lock()
	ls := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()

	if ls != nil {
		ls.closeSubscribers()
	}
}

// LiveCounters returns a session's live packet/byte counters; ok is
// false when the session has no in-memory state (e.g. after a
// controller restart).
func (m *Manager) LiveCounters(id string) (packets, bytes int64, ok bool) {
	m.mu.Lock()
	ls := m.sessions[id]
	m.mu.Unlock()

	if ls == nil {
		return 0, 0, false
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()

	return ls.window.total, ls.bytes, true
}

// Page returns packet rows [offset, offset+limit) from the live
// window; ok is false when the session has no in-memory state.
func (m *Manager) Page(id string, offset int64, limit int) (rows []PacketRow, total, first int64, ok bool) {
	m.mu.Lock()
	ls := m.sessions[id]
	m.mu.Unlock()

	if ls == nil {
		return nil, 0, 0, false
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()

	packets := ls.window.page(offset, limit)
	rows = make([]PacketRow, len(packets))
	for i, p := range packets {
		rows[i] = p.Row
	}

	return rows, ls.window.total, ls.window.first(), true
}

// Packet returns one packet by index from the live window.
func (m *Manager) Packet(id string, index int64) (Packet, bool) {
	m.mu.Lock()
	ls := m.sessions[id]
	m.mu.Unlock()

	if ls == nil {
		return Packet{}, false
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()

	return ls.window.get(index)
}

// Subscribe attaches a live listener: it returns a snapshot of the
// current window and a channel of subsequent events (atomically, so
// no packet is lost or duplicated between the two). ok is false when
// the session has no live state.
func (m *Manager) Subscribe(id string) (snapshot Event, events <-chan Event, cancel func(), ok bool) {
	m.mu.Lock()
	ls := m.sessions[id]
	m.mu.Unlock()

	if ls == nil {
		return Event{}, nil, nil, false
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()

	packets := ls.window.page(ls.window.first(), ls.window.size)
	rows := make([]PacketRow, len(packets))
	for i, p := range packets {
		rows[i] = p.Row
	}

	snapshot = Event{
		Type:           "packets",
		Packets:        rows,
		Total:          ls.window.total,
		FirstAvailable: ls.window.first(),
		State:          ls.state,
	}

	ch := make(chan Event, subscriberBuffer)
	if ls.state != "" {
		// Already finished: the snapshot says so; no events will come.
		close(ch)

		return snapshot, ch, func() {}, true
	}

	ls.subs[ch] = true

	return snapshot, ch, func() {
		ls.mu.Lock()
		defer ls.mu.Unlock()

		if ls.subs[ch] {
			delete(ls.subs, ch)
			close(ch)
		}
	}, true
}

// append records one decoded packet and queues its row for the next
// push. The capture index is assigned here: the packet's position in
// the whole recording.
func (ls *liveSession) append(p Packet, capturedBytes int64) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	p.Row.Index = ls.window.total
	ls.window.append(p)
	ls.bytes += capturedBytes
	ls.pending = append(ls.pending, p.Row)
}

// pushLoop flushes pending rows to subscribers in batches until the
// session ends.
func (ls *liveSession) pushLoop() {
	ticker := time.NewTicker(pushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ls.done:
			ls.flush()

			return
		case <-ticker.C:
			ls.flush()
		}
	}
}

// flush sends the pending batch to every subscriber. A subscriber
// with a full queue misses the batch; the UI detects the index gap
// and points at the pcap download for the full record.
func (ls *liveSession) flush() {
	ls.mu.Lock()
	pending := ls.pending
	ls.pending = nil

	if len(pending) == 0 {
		ls.mu.Unlock()

		return
	}

	ev := Event{
		Type:           "packets",
		Packets:        pending,
		Total:          ls.window.total,
		FirstAvailable: ls.window.first(),
	}

	subs := make([]chan Event, 0, len(ls.subs))
	for ch := range ls.subs {
		subs = append(subs, ch)
	}

	ls.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// finish marks the session ended and tells subscribers.
func (ls *liveSession) finish(state string) {
	ls.mu.Lock()
	ls.state = state
	subs := make([]chan Event, 0, len(ls.subs))
	for ch := range ls.subs {
		subs = append(subs, ch)
	}

	ls.mu.Unlock()

	ev := Event{Type: "end", State: state}
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (ls *liveSession) closeSubscribers() {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	for ch := range ls.subs {
		delete(ls.subs, ch)
		close(ch)
	}
}

func (ls *liveSession) userStopped() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	return ls.stopped
}

// Start begins the retention sweep; it satisfies the Kratos
// transport.Server interface so the loop follows the app lifecycle.
func (m *Manager) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.gcCancel = cancel
	m.gcDone = make(chan struct{})

	go func() {
		defer close(m.gcDone)

		ticker := time.NewTicker(gcInterval)
		defer ticker.Stop()

		m.sweepExpired()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sweepExpired()
			}
		}
	}()

	return nil
}

// Stop ends the retention sweep.
func (m *Manager) Stop(ctx context.Context) error {
	if m.gcCancel != nil {
		m.gcCancel()
		<-m.gcDone
	}

	return nil
}

// sweepExpired deletes recordings past retention. It works purely on
// files: a session whose recording expired stays listable (its
// parameters and counters live in the database), only the packet
// data is gone.
func (m *Manager) sweepExpired() {
	pattern := filepath.Join(m.dataDir, "labs", "*", "captures", "*.pcapng")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-retention)
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}

		id := filepath.Base(f)
		if m.isLive(id[:len(id)-len(".pcapng")]) {
			continue
		}

		if err := os.Remove(f); err != nil {
			m.log.Warn("capture: remove expired recording", "file", f, "error", err)
		} else {
			m.log.Info("capture: expired recording removed", "file", f)
		}
	}
}

func (m *Manager) isLive(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ls, ok := m.sessions[id]

	return ok && ls.state == ""
}
