package data

import (
	"fmt"
	"log/slog"

	"github.com/ifantsai/dcnetlab/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// NewRuntimeDriver selects the runtime driver from config: containerlab
// when requested or auto-detected in PATH, otherwise the noop driver
// that only renders artifacts.
func NewRuntimeDriver(c *conf.Data, log *slog.Logger) (runtime.Driver, error) {
	clab := &runtime.ContainerlabDriver{}
	switch c.Runtime {
	case "containerlab":
		if !clab.Available() {
			return nil, fmt.Errorf("containerlab binary not found in PATH")
		}

		return clab, nil
	case "noop":
		return runtime.NoopDriver{}, nil
	case "auto":
		if clab.Available() {
			return clab, nil
		}

		log.Warn("containerlab not found, falling back to noop runtime; artifacts will be generated but not deployed")

		return runtime.NoopDriver{}, nil
	default:
		return nil, fmt.Errorf("unknown runtime driver %q", c.Runtime)
	}
}
