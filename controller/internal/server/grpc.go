package server

import (
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	kgrpc "github.com/go-kratos/kratos/v2/transport/grpc"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/controller/internal/service"
	v1 "github.com/ifantsai/dcnetlab/pb/dcnetlab/v1"
)

// NewGRPCServer builds the Kratos gRPC server for the same service as
// the HTTP transport. It is only started when GRPCAddr is set (see
// newApp in cmd/controller).
func NewGRPCServer(c *conf.Server, svc *service.DCNetLabService, logger log.Logger) *kgrpc.Server {
	srv := kgrpc.NewServer(
		kgrpc.Address(c.GRPCAddr),
		kgrpc.Middleware(recovery.Recovery(), logging.Server(logger)),
	)
	v1.RegisterDCNetLabServer(srv, svc)

	return srv
}
