package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/agentdriver"
	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// pingTimeout bounds the startup probe of the data plane agent.
const pingTimeout = 3 * time.Second

// NewRuntimeDriver selects the runtime driver from config: the
// data plane agent when requested or reachable in auto mode, otherwise
// the noop driver that only renders artifacts. "containerlab" is
// accepted as an alias from before the agent split.
func NewRuntimeDriver(c *conf.Data, log *slog.Logger) (runtime.Driver, func(), error) {
	noCleanup := func() {}
	if c.Runtime == "noop" {
		return runtime.NoopDriver{}, noCleanup, nil
	}

	if c.Runtime != "auto" && c.Runtime != "agent" && c.Runtime != "containerlab" {
		return nil, nil, fmt.Errorf("unknown runtime driver %q", c.Runtime)
	}

	driver, cleanup, err := agentdriver.New(c.AgentAddr, c.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("agent driver: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	available, detail, err := driver.Ping(ctx)
	switch {
	case err == nil && available:
		return driver, cleanup, nil
	case c.Runtime == "auto":
		if err != nil {
			log.Warn("agent unreachable, falling back to noop runtime; artifacts will be generated but not deployed",
				"addr", c.AgentAddr, "error", err)
		} else {
			log.Warn("agent runtime unusable, falling back to noop runtime", "detail", detail)
		}

		cleanup()

		return runtime.NoopDriver{}, noCleanup, nil
	case err != nil:
		cleanup()

		return nil, nil, fmt.Errorf("agent at %s unreachable: %w", c.AgentAddr, err)
	default:
		cleanup()

		return nil, nil, fmt.Errorf("agent runtime unusable: %s", detail)
	}
}
