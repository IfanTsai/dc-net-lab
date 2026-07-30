//go:build wireinject
// +build wireinject

// The build tag makes sure the stub is not built in the final build.

package main

import (
	"log/slog"

	"github.com/go-kratos/kratos/v2"
	klog "github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"

	"github.com/ifantsai/dcnetlab/controller/internal/biz"
	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/controller/internal/data"
	"github.com/ifantsai/dcnetlab/controller/internal/metrics"
	"github.com/ifantsai/dcnetlab/controller/internal/observer"
	"github.com/ifantsai/dcnetlab/controller/internal/server"
	"github.com/ifantsai/dcnetlab/controller/internal/service"
	"github.com/ifantsai/dcnetlab/controller/internal/traffic"
)

// wireApp builds the controller object graph from the per-layer
// ProviderSets. Regenerate wire_gen.go with `make wire`.
func wireApp(*conf.Server, *conf.Data, klog.Logger, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		data.ProviderSet, biz.ProviderSet, service.ProviderSet, server.ProviderSet,
		observer.New,
		metrics.NewHistory,
		metrics.NewCollector,
		traffic.NewHistory,
		traffic.NewCollector,
		wire.Bind(new(server.TerminalOpener), new(*biz.TerminalUsecase)),
		wire.Bind(new(server.TopologyWatcher), new(*observer.Observer)),
		wire.Bind(new(server.MetricsSource), new(*metrics.History)),
		wire.Bind(new(server.CaptureFeed), new(*biz.CaptureUsecase)),
		wire.Bind(new(biz.MetricsHistory), new(*metrics.History)),
		wire.Bind(new(biz.TrafficHistory), new(*traffic.History)),
		wire.Bind(new(traffic.Stopper), new(*biz.TrafficUsecase)),
		newApp,
	))
}
