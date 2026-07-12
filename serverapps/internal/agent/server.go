package agent

import (
	"context"
	"fmt"

	pb "github.com/ifantsai/dcnetlab/pb/nodeagent/v1"
)

// tail length defaults and bounds for TailLogs.
const (
	defaultTailLines = 200
	maxTailLines     = 2000
)

// Service exposes the Manager as the ServerAgent gRPC service.
type Service struct {
	pb.UnimplementedNodeAgentServer

	mgr *Manager
}

// NewService wires the gRPC service around a manager.
func NewService(mgr *Manager) *Service { return &Service{mgr: mgr} }

func (s *Service) InstallPackage(ctx context.Context, req *pb.InstallPackageRequest) (*pb.InstallPackageReply, error) {
	if req.Package == nil {
		return nil, fmt.Errorf("package is required")
	}

	ref := PackageRef{
		Name:       req.Package.Name,
		Version:    req.Package.Version,
		SHA256:     req.Package.Sha256,
		URL:        req.Package.Url,
		Entrypoint: req.Package.Entrypoint,
	}
	if err := s.mgr.InstallPackage(ctx, ref); err != nil {
		return nil, err
	}

	return &pb.InstallPackageReply{}, nil
}

func (s *Service) RemovePackage(ctx context.Context, req *pb.RemovePackageRequest) (*pb.RemovePackageReply, error) {
	removed, inUse, err := s.mgr.RemovePackage(req.Name, req.Version)
	if err != nil {
		return nil, err
	}

	reply := &pb.RemovePackageReply{Removed: removed}
	for _, u := range inUse {
		reply.InUse = append(reply.InUse, &pb.InUseVersion{Version: u.Version, Programs: u.Programs})
	}

	return reply, nil
}

func (s *Service) Install(ctx context.Context, req *pb.InstallRequest) (*pb.ProgramInfo, error) {
	if req.Spec == nil {
		return nil, fmt.Errorf("spec is required")
	}

	info, err := s.mgr.Install(specFromPB(req.Spec))
	if err != nil {
		return nil, err
	}

	return infoToPB(info), nil
}

func (s *Service) Start(ctx context.Context, req *pb.ProgramRef) (*pb.ProgramInfo, error) {
	info, err := s.mgr.Start(req.Name)
	if err != nil {
		return nil, err
	}

	return infoToPB(info), nil
}

func (s *Service) Stop(ctx context.Context, req *pb.ProgramRef) (*pb.ProgramInfo, error) {
	info, err := s.mgr.Stop(req.Name)
	if err != nil {
		return nil, err
	}

	return infoToPB(info), nil
}

func (s *Service) Remove(ctx context.Context, req *pb.ProgramRef) (*pb.RemoveReply, error) {
	if err := s.mgr.Remove(req.Name); err != nil {
		return nil, err
	}

	return &pb.RemoveReply{}, nil
}

func (s *Service) List(ctx context.Context, _ *pb.ListRequest) (*pb.ListReply, error) {
	reply := &pb.ListReply{}
	for _, info := range s.mgr.List() {
		reply.Programs = append(reply.Programs, infoToPB(info))
	}

	return reply, nil
}

func (s *Service) ListPackages(ctx context.Context, _ *pb.ListPackagesRequest) (*pb.ListPackagesReply, error) {
	packages, err := s.mgr.ListPackages()
	if err != nil {
		return nil, err
	}

	reply := &pb.ListPackagesReply{}
	for _, p := range packages {
		reply.Packages = append(reply.Packages, &pb.InstalledPackage{
			Name: p.Name, Version: p.Version, Sha256: p.SHA256,
		})
	}

	return reply, nil
}

func (s *Service) TailLogs(ctx context.Context, req *pb.TailLogsRequest) (*pb.TailLogsReply, error) {
	lines := int(req.Lines)
	if lines <= 0 {
		lines = defaultTailLines
	}

	if lines > maxTailLines {
		lines = maxTailLines
	}

	content, err := s.mgr.TailLogs(req.Name, lines)
	if err != nil {
		return nil, err
	}

	return &pb.TailLogsReply{Content: content}, nil
}

func specFromPB(spec *pb.ProgramSpec) Spec {
	return Spec{
		Name:           spec.Name,
		PackageName:    spec.PackageName,
		PackageVersion: spec.PackageVersion,
		Entrypoint:     spec.Entrypoint,
		Args:           spec.Args,
		RestartPolicy:  spec.RestartPolicy,
		Type:           spec.Type,
		AutoStart:      spec.AutoStart,
	}
}

func infoToPB(info Info) *pb.ProgramInfo {
	pbInfo := &pb.ProgramInfo{
		Spec: &pb.ProgramSpec{
			Name:           info.Spec.Name,
			PackageName:    info.Spec.PackageName,
			PackageVersion: info.Spec.PackageVersion,
			Entrypoint:     info.Spec.Entrypoint,
			Args:           info.Spec.Args,
			RestartPolicy:  info.Spec.RestartPolicy,
			Type:           info.Spec.Type,
			AutoStart:      info.Spec.AutoStart,
		},
		State:     info.State,
		Pid:       int32(info.PID),
		Restarts:  int32(info.Restarts),
		LastError: info.LastError,
	}

	if !info.StartedAt.IsZero() {
		pbInfo.StartedAt = info.StartedAt.Unix()
	}

	return pbInfo
}
