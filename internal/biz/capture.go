package biz

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"regexp"
	"time"

	"github.com/ifantsai/dcnetlab/internal/capture"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// Capture policy limits (design §16.5).
const (
	captureDefaultSnap     = 256
	captureMaxSnap         = 65535
	captureDefaultDuration = 30 * time.Second
	captureMaxDuration     = 10 * time.Minute
	captureMaxBytes        = 100 << 20 // per-recording cap
	captureMaxPerNode      = 2
	capturePageLimit       = 500
)

var captureNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// CaptureRepo abstracts the persistence the capture usecase needs.
type CaptureRepo interface {
	CreateCaptureSession(c *model.CaptureSession) error
	UpdateCaptureSession(c *model.CaptureSession) error
	DeleteCaptureSession(id string) error
	GetCaptureSession(id string) (*model.CaptureSession, error)
	ListCaptureSessions(labID string) ([]*model.CaptureSession, error)
	ListAllCaptureSessions() ([]*model.CaptureSession, error)
	GetLab(id string) (*model.Lab, error)
	ListNodes(labID string) ([]*model.Node, error)
	ListLinks(labID string) ([]*model.Link, error)
}

// CaptureUsecase manages CaptureSessions: packet recordings on one
// modelled interface of a lab node. A session starts capturing on
// creation, ends by itself (duration/limits) or via stop, and its
// recording stays downloadable until deleted or expired. The capture
// manager owns the live pipeline; this usecase owns validation and
// the resource state.
type CaptureUsecase struct {
	repo CaptureRepo
	mgr  *capture.Manager
	log  *slog.Logger
}

// NewCaptureUsecase wires the capture usecase. Sessions still marked
// Running in the database lost their pipeline when the controller
// went down, so they are failed right here — before any API traffic
// can observe a Running session nothing is capturing for.
func NewCaptureUsecase(repo CaptureRepo, mgr *capture.Manager, log *slog.Logger) (*CaptureUsecase, error) {
	uc := &CaptureUsecase{repo: repo, mgr: mgr, log: log}

	sessions, err := repo.ListAllCaptureSessions()
	if err != nil {
		return nil, fmt.Errorf("list capture sessions: %w", err)
	}

	for _, s := range sessions {
		if s.Status.State != model.CaptureStateRunning {
			continue
		}

		s.Status.State = model.CaptureStateFailed
		s.Status.LastError = "controller restarted during capture"
		s.Status.EndedAt = time.Now().UTC()
		if err := repo.UpdateCaptureSession(s); err != nil {
			return nil, fmt.Errorf("fail interrupted capture session: %w", err)
		}
	}

	return uc, nil
}

// CreateCaptureSession validates, persists and immediately starts a
// capture. A start failure is recorded on the session and returned.
func (uc *CaptureUsecase) CreateCaptureSession(ctx context.Context, labID, name string, spec model.CaptureSessionSpec) (*model.CaptureSession, error) {
	if !captureNameRE.MatchString(name) {
		return nil, fmt.Errorf("invalid session name %q (lowercase letters, digits, dashes, max 63 chars)", name)
	}

	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	spec.LabID = labID
	if err := uc.normaliseSpec(&spec); err != nil {
		return nil, err
	}

	if err := uc.checkNodeBudget(spec); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	sess := &model.CaptureSession{
		Meta: model.ResourceMeta{
			ID: model.NewID("cap"), Name: name, CreatedAt: now, UpdatedAt: now,
		},
		Spec: spec,
		Status: model.CaptureSessionStatus{
			State:     model.CaptureStateRunning,
			StartedAt: now,
		},
	}

	if err := uc.repo.CreateCaptureSession(sess); err != nil {
		return nil, fmt.Errorf("persist capture session: %w", err)
	}

	if err := uc.mgr.StartSession(lab.Meta.Name, sess, func(res capture.Result) {
		uc.captureEnded(sess.Meta.ID, res)
	}); err != nil {
		sess.Status.State = model.CaptureStateFailed
		sess.Status.LastError = err.Error()
		sess.Status.EndedAt = time.Now().UTC()
		if uerr := uc.repo.UpdateCaptureSession(sess); uerr != nil {
			uc.log.Warn("capture: record start failure", "session", name, "error", uerr)
		}

		return nil, err
	}

	return sess, nil
}

// normaliseSpec resolves the target, applies policy defaults and
// rejects out-of-range values.
func (uc *CaptureUsecase) normaliseSpec(spec *model.CaptureSessionSpec) error {
	node, err := uc.nodeByID(spec.LabID, spec.NodeID)
	if err != nil {
		return err
	}

	spec.NodeName = node.Meta.Name
	if err := uc.checkInterface(node, spec.Interface); err != nil {
		return err
	}

	switch spec.Direction {
	case "":
		spec.Direction = model.CaptureDirectionBoth
	case model.CaptureDirectionBoth, model.CaptureDirectionRx, model.CaptureDirectionTx:
	default:
		return fmt.Errorf("invalid direction %q (want both, rx or tx)", spec.Direction)
	}

	if spec.SnapLength == 0 {
		spec.SnapLength = captureDefaultSnap
	}

	if spec.SnapLength < 64 || spec.SnapLength > captureMaxSnap {
		return fmt.Errorf("snapLength %d out of range [64, %d]", spec.SnapLength, captureMaxSnap)
	}

	if spec.Duration == 0 {
		spec.Duration = captureDefaultDuration
	}

	if spec.Duration < time.Second || spec.Duration > captureMaxDuration {
		return fmt.Errorf("duration %s out of range [1s, %s]", spec.Duration, captureMaxDuration)
	}

	if spec.MaxBytes == 0 || spec.MaxBytes > captureMaxBytes {
		spec.MaxBytes = captureMaxBytes
	}

	return validateCaptureFilter(spec.Filter)
}

// checkInterface enforces the simulation-view scope: only modelled
// interfaces are capture targets — the node's link endpoints plus its
// logical interfaces (gateway vlanif on leaves, bond0 on servers) —
// mirroring the observer's interface accounting. Container plumbing
// (eth0, br0, VRRP macvlan) is not part of the simulated device.
func (uc *CaptureUsecase) checkInterface(node *model.Node, iface string) error {
	if iface == "" {
		return fmt.Errorf("interface is required")
	}

	links, err := uc.repo.ListLinks(node.Spec.LabID)
	if err != nil {
		return fmt.Errorf("list links: %w", err)
	}

	allowed := make([]string, 0, 8)
	for _, l := range links {
		for _, ep := range []model.LinkEndpoint{l.Spec.EndpointA, l.Spec.EndpointB} {
			if ep.NodeID == node.Meta.ID {
				allowed = append(allowed, ep.Interface)
			}
		}
	}

	switch {
	case node.Spec.VlanID != 0:
		allowed = append(allowed, fmt.Sprintf("vlan%d", node.Spec.VlanID))
	case node.Spec.Role == model.RoleServer && node.Spec.Address.IsValid():
		allowed = append(allowed, "bond0")
	}

	for _, a := range allowed {
		if a == iface {
			return nil
		}
	}

	return fmt.Errorf("interface %q is not a modelled interface of %s (have %v)", iface, node.Meta.Name, allowed)
}

func validateCaptureFilter(f model.CaptureFilter) error {
	switch f.Protocol {
	case "", model.CaptureProtocolARP, model.CaptureProtocolICMP, model.CaptureProtocolTCP,
		model.CaptureProtocolUDP, model.CaptureProtocolBGP, model.CaptureProtocolVXLAN:
	default:
		return fmt.Errorf("invalid protocol filter %q", f.Protocol)
	}

	for _, p := range []struct{ name, value string }{
		{"srcPrefix", f.SrcPrefix}, {"dstPrefix", f.DstPrefix},
	} {
		if p.value == "" {
			continue
		}

		if _, err := netip.ParsePrefix(p.value); err != nil {
			if _, err := netip.ParseAddr(p.value); err != nil {
				return fmt.Errorf("%s %q is neither a prefix nor an address", p.name, p.value)
			}
		}
	}

	if f.Port < 0 || f.Port > 65535 {
		return fmt.Errorf("port %d out of range", f.Port)
	}

	return nil
}

// checkNodeBudget enforces the per-node concurrent session limit.
func (uc *CaptureUsecase) checkNodeBudget(spec model.CaptureSessionSpec) error {
	sessions, err := uc.repo.ListCaptureSessions(spec.LabID)
	if err != nil {
		return fmt.Errorf("list capture sessions: %w", err)
	}

	running := 0
	for _, s := range sessions {
		if s.Spec.NodeID == spec.NodeID && s.Status.State == model.CaptureStateRunning {
			running++
		}
	}

	if running >= captureMaxPerNode {
		return fmt.Errorf("node %s already has %d running captures (max %d)", spec.NodeName, running, captureMaxPerNode)
	}

	return nil
}

// captureEnded persists the outcome reported by the manager's reader
// goroutine. It is the only writer of terminal states besides the
// start-failure path, so the state machine stays single-writer.
func (uc *CaptureUsecase) captureEnded(id string, res capture.Result) {
	sess, err := uc.repo.GetCaptureSession(id)
	if err != nil {
		uc.log.Warn("capture: load session at end", "session", id, "error", err)

		return
	}

	sess.Status.Packets = uint64(res.Packets)
	sess.Status.Bytes = uint64(res.Bytes)
	sess.Status.EndedAt = time.Now().UTC()

	switch {
	case res.UserStopped:
		sess.Status.State = model.CaptureStateStopped
	case res.Err != nil:
		sess.Status.State = model.CaptureStateFailed
		sess.Status.LastError = res.Err.Error()
	default:
		sess.Status.State = model.CaptureStateCompleted
	}

	if err := uc.repo.UpdateCaptureSession(sess); err != nil {
		uc.log.Warn("capture: persist session end", "session", id, "error", err)
	}
}

// StopCaptureSession ends a running capture; stopping a finished one
// is a no-op. It returns the session with its final counters.
func (uc *CaptureUsecase) StopCaptureSession(ctx context.Context, labID, id string) (*model.CaptureSession, error) {
	sess, err := uc.get(labID, id)
	if err != nil {
		return nil, err
	}

	if sess.Status.State != model.CaptureStateRunning {
		return sess, nil
	}

	// Blocks until the reader goroutine has persisted the outcome.
	uc.mgr.StopSession(id)

	return uc.get(labID, id)
}

// DeleteCaptureSession stops the capture if needed and removes the
// session with its recording.
func (uc *CaptureUsecase) DeleteCaptureSession(ctx context.Context, labID, id string) error {
	sess, err := uc.get(labID, id)
	if err != nil {
		return err
	}

	uc.mgr.Drop(id)

	if err := os.Remove(uc.mgr.FilePath(labID, sess.Meta.ID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove recording: %w", err)
	}

	return uc.repo.DeleteCaptureSession(id)
}

// ListCaptureSessions returns the sessions of a lab, live counters
// merged in for running ones.
func (uc *CaptureUsecase) ListCaptureSessions(labID string) ([]*model.CaptureSession, error) {
	sessions, err := uc.repo.ListCaptureSessions(labID)
	if err != nil {
		return nil, err
	}

	for _, s := range sessions {
		uc.mergeLive(s)
	}

	return sessions, nil
}

// GetCaptureSession returns one session, live counters merged in.
func (uc *CaptureUsecase) GetCaptureSession(labID, id string) (*model.CaptureSession, error) {
	sess, err := uc.get(labID, id)
	if err != nil {
		return nil, err
	}

	uc.mergeLive(sess)

	return sess, nil
}

func (uc *CaptureUsecase) mergeLive(s *model.CaptureSession) {
	if s.Status.State != model.CaptureStateRunning {
		return
	}

	if packets, bytes, ok := uc.mgr.LiveCounters(s.Meta.ID); ok {
		s.Status.Packets = uint64(packets)
		s.Status.Bytes = uint64(bytes)
	}
}

// CapturePackets pages through a session's packet rows: from the live
// window when the session still has in-memory state, otherwise from
// the recording file.
func (uc *CaptureUsecase) CapturePackets(labID, id string, offset int64, limit int) (rows []capture.PacketRow, total, first int64, err error) {
	sess, err := uc.get(labID, id)
	if err != nil {
		return nil, 0, 0, err
	}

	if limit <= 0 || limit > capturePageLimit {
		limit = capturePageLimit
	}

	if rows, total, first, ok := uc.mgr.Page(id, offset, limit); ok {
		return rows, total, first, nil
	}

	rows, total, err = capture.ReadPage(uc.mgr.FilePath(labID, sess.Meta.ID), offset, limit)
	if err != nil {
		return nil, 0, 0, err
	}

	return rows, total, 0, nil
}

// CapturePacket returns one packet's decoded layer tree and raw
// bytes.
func (uc *CaptureUsecase) CapturePacket(labID, id string, index int64) (capture.PacketRow, []capture.Layer, []byte, error) {
	sess, err := uc.get(labID, id)
	if err != nil {
		return capture.PacketRow{}, nil, nil, err
	}

	p, ok := uc.mgr.Packet(id, index)
	if !ok {
		p, err = capture.ReadPacket(uc.mgr.FilePath(labID, sess.Meta.ID), index)
		if err != nil {
			return capture.PacketRow{}, nil, nil, err
		}
	}

	return p.Row, capture.Tree(p.Data), p.Data, nil
}

// RecordingPath returns the pcapng file of a session for download,
// with its suggested filename.
func (uc *CaptureUsecase) RecordingPath(labID, id string) (path, filename string, err error) {
	sess, err := uc.get(labID, id)
	if err != nil {
		return "", "", err
	}

	path = uc.mgr.FilePath(labID, sess.Meta.ID)
	if _, err := os.Stat(path); err != nil {
		return "", "", capture.ErrRecordingGone
	}

	return path, sess.Meta.Name + ".pcapng", nil
}

// SubscribeCapture attaches a live listener to a session: an initial
// snapshot plus a channel of subsequent events. For sessions without
// live state (finished before a controller restart) the snapshot
// comes from the recording file and the channel is already closed.
func (uc *CaptureUsecase) SubscribeCapture(labID, id string) (capture.Event, <-chan capture.Event, func(), error) {
	sess, err := uc.get(labID, id)
	if err != nil {
		return capture.Event{}, nil, nil, err
	}

	if snapshot, events, cancel, ok := uc.mgr.Subscribe(id); ok {
		return snapshot, events, cancel, nil
	}

	rows, total, err := capture.ReadPage(uc.mgr.FilePath(labID, sess.Meta.ID), 0, capturePageLimit)
	if err != nil {
		return capture.Event{}, nil, nil, err
	}

	closed := make(chan capture.Event)
	close(closed)

	return capture.Event{
		Type:    "packets",
		Packets: rows,
		Total:   total,
		State:   sess.Status.State,
	}, closed, func() {}, nil
}

// nodeByID resolves a node of the lab.
func (uc *CaptureUsecase) nodeByID(labID, nodeID string) (*model.Node, error) {
	nodes, err := uc.repo.ListNodes(labID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	for _, n := range nodes {
		if n.Meta.ID == nodeID {
			return n, nil
		}
	}

	return nil, fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
}

// get loads a session of the lab, translating a lab mismatch into
// ErrNotFound like the fault usecase does.
func (uc *CaptureUsecase) get(labID, id string) (*model.CaptureSession, error) {
	sess, err := uc.repo.GetCaptureSession(id)
	if err != nil {
		return nil, fmt.Errorf("get capture session: %w", err)
	}

	if sess.Spec.LabID != labID {
		return nil, fmt.Errorf("capture session %q: %w", id, ErrNotFound)
	}

	return sess, nil
}
