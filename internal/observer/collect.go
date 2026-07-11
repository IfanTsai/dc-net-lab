package observer

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// deepScript gathers all exec-based metrics in a single docker exec:
// interface table, BGP summary and route summary, separated so the
// output can be split without ambiguity.
const deepScript = `ip -br link; echo __SEP__; ` +
	`vtysh -c 'show bgp summary json'; echo __SEP__; ` +
	`vtysh -c 'show ip route summary json'`

// deepWorkers bounds the concurrent docker execs of one deep sweep.
const deepWorkers = 8

// collectDeep execs into every running node of the lab and refreshes
// the cached deep metrics. Nodes that are not running are skipped
// (exec would hang on a paused container); their stale metrics are
// dropped so the UI does not show live-looking numbers for a frozen
// device.
func (o *Observer) collectDeep(ctx context.Context, lab *model.Lab, nodes []*model.Node, states map[string]string) {
	metrics := make(map[string]deepMetrics, len(nodes))

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, deepWorkers)
	)
	for _, n := range nodes {
		if states[n.Meta.Name] != "running" {
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(n *model.Node) {
			defer wg.Done()
			defer func() { <-sem }()

			execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			out, err := o.driver.Exec(execCtx, lab.Meta.Name, n.Meta.Name, []string{"sh", "-c", deepScript})
			if err != nil {
				o.log.Debug("observer: deep exec", "node", n.Meta.Name, "error", err)

				return
			}

			m := parseDeepOutput(out)

			mu.Lock()
			metrics[n.Meta.Name] = m
			mu.Unlock()
		}(n)
	}

	wg.Wait()

	o.mu.Lock()
	o.deep[lab.Meta.ID] = metrics
	o.lastDeep[lab.Meta.ID] = time.Now()
	o.mu.Unlock()
}

// parseDeepOutput splits the combined script output back into the
// three metric sections.
func parseDeepOutput(out []byte) deepMetrics {
	var m deepMetrics

	parts := strings.Split(string(out), "__SEP__")
	if len(parts) > 0 {
		m.interfacesUp, m.interfacesTotal = parseInterfaces(parts[0])
	}

	if len(parts) > 1 {
		m.bgpEstablished, m.bgpConfigured = parseBGPSummary([]byte(parts[1]))
	}

	if len(parts) > 2 {
		m.routeCount = parseRouteSummary([]byte(parts[2]))
	}

	return m
}

// parseInterfaces counts fabric-facing interfaces from `ip -br link`
// output; loopback and the management interface are excluded.
func parseInterfaces(s string) (up, total int) {
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		name, _, _ := strings.Cut(fields[0], "@")
		if name == "lo" || name == "eth0" {
			continue
		}

		total++
		if fields[1] == "UP" {
			up++
		}
	}

	return up, total
}

// parseBGPSummary counts configured and established IPv4 unicast
// peers from `show bgp summary json`.
func parseBGPSummary(out []byte) (established, configured int) {
	var summary struct {
		IPv4Unicast struct {
			Peers map[string]struct {
				State string `json:"state"`
			} `json:"peers"`
		} `json:"ipv4Unicast"`
	}

	if err := json.Unmarshal(jsonBody(out), &summary); err != nil {
		return 0, 0
	}

	for _, p := range summary.IPv4Unicast.Peers {
		configured++
		if p.State == "Established" {
			established++
		}
	}

	return established, configured
}

// parseRouteSummary extracts the total RIB size from
// `show ip route summary json`.
func parseRouteSummary(out []byte) int {
	var summary struct {
		RoutesTotal int `json:"routesTotal"`
	}

	if err := json.Unmarshal(jsonBody(out), &summary); err != nil {
		return 0
	}

	return summary.RoutesTotal
}

// jsonBody skips any vtysh warnings printed before the JSON payload.
func jsonBody(out []byte) []byte {
	if i := bytes.IndexByte(out, '{'); i > 0 {
		return out[i:]
	}

	return out
}
