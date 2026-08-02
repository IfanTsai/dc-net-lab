package biz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ifantsai/dcnetlab/controller/internal/operation"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

const (
	controlPlaneTimeout = 3 * time.Minute
	controlPlanePoll    = 3 * time.Second
	dataPlaneAttempts   = 3
	// validateConcurrency bounds the parallel in-container execs the
	// validators fan out (pings, vtysh queries). The execs are
	// independent docker execs on different containers; the bound
	// keeps the docker daemon comfortable.
	validateConcurrency = 8
)

// bgpSummary mirrors the subset of `show bgp summary json` we need.
type bgpSummary struct {
	IPv4Unicast struct {
		Peers map[string]struct {
			State string `json:"state"`
		} `json:"peers"`
	} `json:"ipv4Unicast"`
}

// expectedSessions derives, per node, how many Established BGP
// sessions the desired topology implies: one per fabric link end,
// plus for leaves the iBGP MLAG peer and the rack's servers (dynamic
// peers via listen range), and for servers their two leaf peers.
func expectedSessions(nodes []*model.Node, links []*model.Link) map[string]int {
	expected := make(map[string]int)
	for _, l := range links {
		if l.Spec.Kind != model.LinkFabric {
			continue
		}

		expected[l.Spec.EndpointA.NodeName]++
		expected[l.Spec.EndpointB.NodeName]++
	}

	serversPerRack := make(map[string]int)
	for _, n := range nodes {
		if n.Spec.Role == model.RoleServer {
			serversPerRack[n.Spec.RackID]++
		}
	}

	for _, n := range nodes {
		switch n.Spec.Role {
		case model.RoleServer:
			expected[n.Meta.Name] += len(n.Spec.BGPPeers)
		case model.RoleLeaf:
			expected[n.Meta.Name] += 1 + serversPerRack[n.Spec.RackID] // MLAG iBGP + dynamic server peers
		}
	}

	return expected
}

// validateControlPlane waits until every FRR node has at least its
// expected number of BGP sessions and none of them is down. Between
// polls it reports the converged share as the step's progress.
func (uc *PlanUsecase) validateControlPlane(ctx context.Context, lab *model.Lab, nodes []*model.Node, links []*model.Link) error {
	expected := expectedSessions(nodes, links)
	routers := 0
	for _, n := range nodes {
		if n.Spec.IsRouter() && expected[n.Meta.Name] > 0 {
			routers++
		}
	}

	deadline := time.Now().Add(controlPlaneTimeout)
	var lastPending []string
	for {
		pending, err := uc.bgpUnconverged(ctx, lab, nodes, expected)
		if errors.Is(err, runtime.ErrNotSupported) {
			uc.log.Info("control-plane validation skipped", "runtime", uc.driver.Name())

			return nil
		}

		// A runtime-level failure will not heal by polling; surface it
		// immediately instead of burning the whole timeout.
		if errors.Is(err, runtime.ErrUnavailable) {
			return err
		}

		if err == nil && len(pending) == 0 {
			return nil
		}

		if err != nil {
			// Containers may still be starting; keep polling.
			pending = []string{err.Error()}
		} else if routers > 0 {
			operation.Report(ctx, float64(routers-len(pending))/float64(routers))
		}

		lastPending = pending
		if time.Now().After(deadline) {
			return fmt.Errorf("BGP did not converge within %s: %s",
				controlPlaneTimeout, strings.Join(lastPending, "; "))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(controlPlanePoll):
		}
	}
}

// bgpUnconverged returns a description of every FRR node whose BGP
// sessions are not all Established yet. The per-node vtysh queries
// are independent and run concurrently.
func (uc *PlanUsecase) bgpUnconverged(ctx context.Context, lab *model.Lab, nodes []*model.Node, expected map[string]int) ([]string, error) {
	var (
		mu      sync.Mutex
		pending []string
	)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(validateConcurrency)

	report := func(msg string) {
		mu.Lock()
		defer mu.Unlock()

		pending = append(pending, msg)
	}

	for _, n := range nodes {
		if !n.Spec.IsRouter() {
			continue
		}

		want := expected[n.Meta.Name]
		if want == 0 {
			continue
		}

		g.Go(func() error {
			out, err := uc.driver.Exec(ctx, lab.Meta.Name, n.Meta.Name,
				[]string{"vtysh", "-c", "show bgp summary json"})
			if errors.Is(err, runtime.ErrNotSupported) || errors.Is(err, runtime.ErrUnavailable) {
				return err
			}

			if err != nil {
				report(fmt.Sprintf("%s: %v", n.Meta.Name, err))

				return nil
			}

			// vtysh may print warnings before the JSON body; skip to it.
			if i := bytes.IndexByte(out, '{'); i > 0 {
				out = out[i:]
			}

			var sum bgpSummary
			if err := json.Unmarshal(out, &sum); err != nil {
				report(fmt.Sprintf("%s: bad bgp summary: %v", n.Meta.Name, err))

				return nil
			}

			established := 0
			for _, p := range sum.IPv4Unicast.Peers {
				if p.State == "Established" {
					established++
				}
			}

			if established < want {
				report(fmt.Sprintf("%s: %d/%d established", n.Meta.Name, established, want))
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	sort.Strings(pending)

	return pending, nil
}

// validateDataPlane pings server to server across the fabric: each
// server pings the next one (by name), so every rack, leaf pair and
// pod boundary is crossed at least once. The per-server checks are
// independent and run concurrently; each finished server bumps the
// step's progress.
func (uc *PlanUsecase) validateDataPlane(ctx context.Context, lab *model.Lab, nodes []*model.Node) error {
	var servers []*model.Node
	for _, n := range nodes {
		if n.Spec.Role == model.RoleServer && n.Spec.Address.IsValid() {
			servers = append(servers, n)
		}
	}

	if len(servers) < 2 {
		return nil
	}

	sort.Slice(servers, func(i, j int) bool { return servers[i].Meta.Name < servers[j].Meta.Name })

	var finished atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(validateConcurrency)

	for i, src := range servers {
		dst := servers[(i+1)%len(servers)]
		g.Go(func() error {
			if err := uc.pingTargets(gctx, lab, src, dst); err != nil {
				return err
			}

			operation.Report(ctx, float64(finished.Add(1))/float64(len(servers)))

			return nil
		})
	}

	err := g.Wait()
	if errors.Is(err, runtime.ErrNotSupported) {
		uc.log.Info("data-plane validation skipped", "runtime", uc.driver.Name())

		return nil
	}

	return err
}

// pingTargets runs one server's data-plane checks: the VRRP virtual
// gateway first (exercises the leaf MLAG pair), then the next server
// (exercises the fabric when the target is in another rack).
func (uc *PlanUsecase) pingTargets(ctx context.Context, lab *model.Lab, src, dst *model.Node) error {
	targets := []struct {
		name string
		addr string
	}{}
	if src.Spec.DefaultGateway.IsValid() {
		targets = append(targets, struct{ name, addr string }{"virtual gateway", src.Spec.DefaultGateway.String()})
	}

	targets = append(targets, struct{ name, addr string }{dst.Meta.Name, dst.Spec.Address.Addr().String()})

	for _, target := range targets {
		var lastErr error
		for attempt := 0; attempt < dataPlaneAttempts; attempt++ {
			_, err := uc.driver.Exec(ctx, lab.Meta.Name, src.Meta.Name,
				[]string{"ping", "-c", "2", "-i", "0.2", "-W", "2", target.addr})
			if errors.Is(err, runtime.ErrNotSupported) {
				return err
			}

			// Runtime-level failures do not heal between attempts.
			if errors.Is(err, runtime.ErrUnavailable) {
				return fmt.Errorf("ping %s -> %s (%s): %w",
					src.Meta.Name, target.name, target.addr, err)
			}

			if err == nil {
				lastErr = nil
				break
			}

			lastErr = err
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}

		if lastErr != nil {
			return fmt.Errorf("ping %s -> %s (%s): %w",
				src.Meta.Name, target.name, target.addr, lastErr)
		}
	}

	return nil
}
