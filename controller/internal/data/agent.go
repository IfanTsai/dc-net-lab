package data

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ifantsai/dcnetlab/controller/internal/biz"
	"github.com/ifantsai/dcnetlab/controller/internal/metrics"
	"github.com/ifantsai/dcnetlab/controller/internal/traffic"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
	"github.com/ifantsai/dcnetlab/internal/runtime"
	pb "github.com/ifantsai/dcnetlab/pb/nodeagent/v1"
)

// agentCallTimeout bounds every node-agent RPC; package installs
// get a longer budget because the agent downloads the archive first.
const (
	agentCallTimeout   = 5 * time.Second
	packageCallTimeout = 60 * time.Second
)

// programAgent implements biz.ProgramAgent over the NodeAgent gRPC
// API. Connections are dialled per call: calls are rare (UI actions
// and apply restores). When the runtime driver can relay into the lab
// management network (the agent dial proxy), connections run
// through it — the management bridge only exists on the data plane
// machine.
type programAgent struct {
	dialer nodeDialer
	log    *slog.Logger
}

// nodeDialer relays a TCP connection into the lab management network;
// implemented by the remote runtime driver.
type nodeDialer interface {
	DialNode(ctx context.Context, addr string) (net.Conn, error)
}

// NewProgramAgent provides the node-agent client. The driver supplies
// the management-network relay when it has one (noop does not).
func NewProgramAgent(driver runtime.Driver, log *slog.Logger) biz.ProgramAgent {
	dialer, _ := driver.(nodeDialer)

	return &programAgent{dialer: dialer, log: log}
}

// NewMetricsAgent exposes the same client as the slice the metrics
// collector needs.
func NewMetricsAgent(a biz.ProgramAgent) metrics.Agent { return a }

// NewTrafficAgent exposes the same client as the slice the traffic
// collector needs.
func NewTrafficAgent(a biz.ProgramAgent) traffic.Agent { return a }

// call dials the agent at addr (management IP, default agent port)
// and runs fn against the stub.
func (a *programAgent) call(ctx context.Context, addr string, timeout time.Duration,
	fn func(ctx context.Context, c pb.NodeAgentClient) error,
) error {
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if a.dialer != nil {
		opts = append(opts, grpc.WithContextDialer(a.dialer.DialNode))
	}

	conn, err := grpc.NewClient(fmt.Sprintf("%s:%d", addr, nodeagentapi.DefaultPort), opts...)
	if err != nil {
		return fmt.Errorf("dial agent %s: %w", addr, err)
	}

	defer func() { _ = conn.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return fn(callCtx, pb.NewNodeAgentClient(conn))
}

func (a *programAgent) InstallPackage(ctx context.Context, addr string, pkg biz.AgentPackage) error {
	return a.call(ctx, addr, packageCallTimeout, func(ctx context.Context, c pb.NodeAgentClient) error {
		_, err := c.InstallPackage(ctx, &pb.InstallPackageRequest{Package: &pb.PackageRef{
			Name:       pkg.Name,
			Version:    pkg.Version,
			Sha256:     pkg.SHA256,
			Url:        pkg.URL,
			Entrypoint: pkg.Entrypoint,
			Links:      pkg.Links,
		}})

		return err
	})
}

func (a *programAgent) Install(ctx context.Context, addr string, p *model.Program) error {
	return a.call(ctx, addr, agentCallTimeout, func(ctx context.Context, c pb.NodeAgentClient) error {
		_, err := c.Install(ctx, &pb.InstallRequest{Spec: &pb.ProgramSpec{
			Name:           p.Meta.Name,
			PackageName:    p.Spec.PackageName,
			PackageVersion: p.Spec.PackageVersion,
			Entrypoint:     p.Spec.Entrypoint,
			Args:           p.Spec.Args,
			RestartPolicy:  p.Spec.RestartPolicy,
			Type:           p.Spec.Type,
			AutoStart:      p.Spec.AutoStart,
			LivenessCheck:  healthCheckToPB(p.Spec.LivenessCheck),
			ReadinessCheck: healthCheckToPB(p.Spec.ReadinessCheck),
			StartupOrder:   int32(p.Spec.StartupOrder),
		}})

		return err
	})
}

// healthCheckToPB maps a model liveness check onto the agent wire type;
// nil stays nil (unmonitored).
func healthCheckToPB(hc *model.HealthCheck) *pb.HealthCheck {
	if hc == nil {
		return nil
	}

	return &pb.HealthCheck{
		Type:             hc.Type,
		Port:             int32(hc.Port),
		Path:             hc.Path,
		IntervalSeconds:  int32(hc.Interval / time.Second),
		TimeoutSeconds:   int32(hc.Timeout / time.Second),
		FailureThreshold: int32(hc.FailureThreshold),
	}
}

func (a *programAgent) Start(ctx context.Context, addr, name string) (model.ProgramStatus, error) {
	return a.programOp(ctx, addr, name, pb.NodeAgentClient.Start)
}

func (a *programAgent) Stop(ctx context.Context, addr, name string) (model.ProgramStatus, error) {
	return a.programOp(ctx, addr, name, pb.NodeAgentClient.Stop)
}

// programOp runs one ProgramRef → ProgramInfo RPC (Start or Stop).
func (a *programAgent) programOp(ctx context.Context, addr, name string,
	op func(pb.NodeAgentClient, context.Context, *pb.ProgramRef, ...grpc.CallOption) (*pb.ProgramInfo, error),
) (model.ProgramStatus, error) {
	var status model.ProgramStatus
	err := a.call(ctx, addr, agentCallTimeout, func(ctx context.Context, c pb.NodeAgentClient) error {
		info, err := op(c, ctx, &pb.ProgramRef{Name: name})
		if err != nil {
			return err
		}

		status = statusFromPB(info)

		return nil
	})

	return status, err
}

func (a *programAgent) Remove(ctx context.Context, addr, name string) error {
	return a.call(ctx, addr, agentCallTimeout, func(ctx context.Context, c pb.NodeAgentClient) error {
		_, err := c.Remove(ctx, &pb.ProgramRef{Name: name})

		return err
	})
}

func (a *programAgent) List(ctx context.Context, addr string) (map[string]model.ProgramStatus, error) {
	statuses := make(map[string]model.ProgramStatus)
	err := a.call(ctx, addr, agentCallTimeout, func(ctx context.Context, c pb.NodeAgentClient) error {
		reply, err := c.List(ctx, &pb.ListRequest{})
		if err != nil {
			return err
		}

		for _, info := range reply.Programs {
			if info.Spec != nil {
				statuses[info.Spec.Name] = statusFromPB(info)
			}
		}

		return nil
	})

	return statuses, err
}

func (a *programAgent) ListPrograms(ctx context.Context, addr string) ([]model.NodeProgram, error) {
	var programs []model.NodeProgram
	err := a.call(ctx, addr, agentCallTimeout, func(ctx context.Context, c pb.NodeAgentClient) error {
		reply, err := c.List(ctx, &pb.ListRequest{})
		if err != nil {
			return err
		}

		for _, info := range reply.Programs {
			if info.Spec == nil {
				continue
			}

			programs = append(programs, model.NodeProgram{
				Name:           info.Spec.Name,
				PackageName:    info.Spec.PackageName,
				PackageVersion: info.Spec.PackageVersion,
				Entrypoint:     info.Spec.Entrypoint,
				Args:           info.Spec.Args,
				RestartPolicy:  info.Spec.RestartPolicy,
				Type:           info.Spec.Type,
				AutoStart:      info.Spec.AutoStart,
				State:          info.State,
				PID:            int(info.Pid),
				Restarts:       int(info.Restarts),
				LastError:      info.LastError,
				Health:         info.Health,
				Ready:          info.Ready,
			})
		}

		return nil
	})

	return programs, err
}

func (a *programAgent) ListPackages(ctx context.Context, addr string) ([]model.InstalledPackage, error) {
	var packages []model.InstalledPackage
	err := a.call(ctx, addr, agentCallTimeout, func(ctx context.Context, c pb.NodeAgentClient) error {
		reply, err := c.ListPackages(ctx, &pb.ListPackagesRequest{})
		if err != nil {
			return err
		}

		for _, p := range reply.Packages {
			packages = append(packages, model.InstalledPackage{
				Name: p.Name, Version: p.Version, SHA256: p.Sha256,
			})
		}

		return nil
	})

	return packages, err
}

func (a *programAgent) TailLogs(ctx context.Context, addr, name string, lines int) (string, error) {
	var content string
	err := a.call(ctx, addr, agentCallTimeout, func(ctx context.Context, c pb.NodeAgentClient) error {
		reply, err := c.TailLogs(ctx, &pb.TailLogsRequest{Name: name, Lines: int32(lines)})
		if err != nil {
			return err
		}

		content = reply.Content

		return nil
	})

	return content, err
}

func statusFromPB(info *pb.ProgramInfo) model.ProgramStatus {
	status := model.ProgramStatus{
		State:     info.State,
		PID:       int(info.Pid),
		Restarts:  int(info.Restarts),
		LastError: info.LastError,
		Health:    info.Health,
		Ready:     info.Ready,
	}

	if info.StartedAt > 0 {
		status.StartedAt = time.Unix(info.StartedAt, 0).UTC()
	}

	return status
}
