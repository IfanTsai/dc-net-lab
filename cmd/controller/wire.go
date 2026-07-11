//go:build wireinject
// +build wireinject

// The build tag makes sure the stub is not built in the final build.

package main

import (
	"log/slog"

	"github.com/go-kratos/kratos/v2"
	klog "github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"

	"github.com/ifantsai/dcnetlab/internal/biz"
	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/data"
	"github.com/ifantsai/dcnetlab/internal/observer"
	"github.com/ifantsai/dcnetlab/internal/server"
	"github.com/ifantsai/dcnetlab/internal/service"
)

// wireApp builds the controller object graph from the per-layer
// ProviderSets. Regenerate wire_gen.go with `make wire`.
func wireApp(*conf.Server, *conf.Data, klog.Logger, *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		data.ProviderSet, biz.ProviderSet, service.ProviderSet, server.ProviderSet,
		observer.New,
		wire.Bind(new(server.TerminalOpener), new(*biz.TerminalUsecase)),
		wire.Bind(new(server.TopologyWatcher), new(*observer.Observer)),
		newApp,
	))
}
