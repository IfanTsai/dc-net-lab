package observer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// deepScript gathers all exec-based metrics in a single docker exec:
// interface table, BGP summary, route summary and VRRP status,
// separated so the output can be split without ambiguity.
const deepScript = `ip -br link; echo __SEP__; ` +
	`vtysh -c 'show bgp summary json'; echo __SEP__; ` +
	`vtysh -c 'show ip route summary json'; echo __SEP__; ` +
	`vtysh -c 'show vrrp json'`

// deepWorkers bounds the concurrent docker execs of one deep sweep.
const deepWorkers = 8

// collectDeep execs into every running node of the lab and refreshes
// the cached deep metrics. Nodes that are not running are skipped
// (exec would hang on a paused container); their stale metrics are
// dropped so the UI does not show live-looking numbers for a frozen
// device.
func (o *Observer) collectDeep(ctx context.Context, lab *model.Lab, nodes []*model.Node, states map[string]string) {
	links, err := o.store.ListLinks(lab.Meta.ID)
	if err != nil {
		o.log.Error("observer: list links", "lab", lab.Meta.Name, "error", err)
	}

	expected := simulatedInterfaces(nodes, links)
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

			m := parseDeepOutput(out, expected[n.Meta.Name])

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

// simulatedInterfaces maps each node name to the interfaces the
// topology model declares for it: its link endpoints (ordered eth1,
// eth2, ... regardless of link creation order) plus the modeled
// logical interfaces (the gateway vlanif on leaves, bond0 on servers)
// last. Only these count towards the interface status shown for a
// device; container plumbing (bridge, VRRP macvlan, management eth0)
// belongs to the node runtime view.
func simulatedInterfaces(nodes []*model.Node, links []*model.Link) map[string][]string {
	names := make(map[string]string, len(nodes)) // node ID → name
	for _, n := range nodes {
		names[n.Meta.ID] = n.Meta.Name
	}

	out := make(map[string][]string, len(nodes))
	add := func(e model.LinkEndpoint) {
		if name, ok := names[e.NodeID]; ok {
			out[name] = append(out[name], e.Interface)
		}
	}

	for _, l := range links {
		add(l.Spec.EndpointA)
		add(l.Spec.EndpointB)
	}

	for _, ifaces := range out {
		sortInterfaces(ifaces)
	}

	for _, n := range nodes {
		switch {
		case n.Spec.VlanID != 0:
			out[n.Meta.Name] = append(out[n.Meta.Name], fmt.Sprintf("vlan%d", n.Spec.VlanID))
		case n.Spec.Role == model.RoleServer && n.Spec.Address.IsValid():
			out[n.Meta.Name] = append(out[n.Meta.Name], "bond0")
		}
	}

	return out
}

// sortInterfaces orders names numerically by trailing index (eth2
// before eth10).
func sortInterfaces(names []string) {
	slices.SortFunc(names, func(a, b string) int {
		pa, na := splitTrailingNum(a)
		pb, nb := splitTrailingNum(b)
		if pa != pb {
			return strings.Compare(pa, pb)
		}

		return na - nb
	})
}

// splitTrailingNum splits "eth10" into ("eth", 10).
func splitTrailingNum(s string) (string, int) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}

	n, _ := strconv.Atoi(s[i:])

	return s[:i], n
}

// parseDeepOutput splits the combined script output back into the
// metric sections; ifaces is the node's simulated interface list.
func parseDeepOutput(out []byte, ifaces []string) deepMetrics {
	var m deepMetrics

	parts := strings.Split(string(out), "__SEP__")
	if len(parts) > 0 {
		m.interfaces = interfaceStatuses(parseInterfaceStates(parts[0]), ifaces)
	}

	if len(parts) > 1 {
		m.bgpEstablished, m.bgpConfigured = parseBGPSummary([]byte(parts[1]))
	}

	if len(parts) > 2 {
		m.routeCount = parseRouteSummary([]byte(parts[2]))
	}

	if len(parts) > 3 {
		m.vrrpState = parseVRRPState([]byte(parts[3]))
	}

	return m
}

// parseInterfaceStates reads `ip -br link` output into a name →
// operational state map (UP, DOWN, UNKNOWN, ...).
func parseInterfaceStates(s string) map[string]string {
	states := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		name, _, _ := strings.Cut(fields[0], "@")
		states[name] = fields[1]
	}

	return states
}

// interfaceStatuses projects the kernel interface states onto the
// simulated interface list; an interface missing from the kernel
// (e.g. its veth is gone) counts as down.
func interfaceStatuses(states map[string]string, ifaces []string) []model.InterfaceStatus {
	if len(ifaces) == 0 {
		return nil
	}

	out := make([]model.InterfaceStatus, 0, len(ifaces))
	for _, name := range ifaces {
		out = append(out, model.InterfaceStatus{Name: name, Up: states[name] == "UP"})
	}

	return out
}

// countUp counts the up interfaces of a simulated interface list.
func countUp(ifaces []model.InterfaceStatus) (up, total int) {
	for _, it := range ifaces {
		if it.Up {
			up++
		}
	}

	return up, len(ifaces)
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

	if err := json.Unmarshal(jsonBody(out, '{'), &summary); err != nil {
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

	if err := json.Unmarshal(jsonBody(out, '{'), &summary); err != nil {
		return 0
	}

	return summary.RoutesTotal
}

// parseVRRPState extracts the IPv4 VRRP role (Master, Backup,
// Initialize) from `show vrrp json`; nodes without vrrpd yield "".
func parseVRRPState(out []byte) string {
	var entries []struct {
		V4 struct {
			Status string `json:"status"`
		} `json:"v4"`
	}

	if err := json.Unmarshal(jsonBody(out, '['), &entries); err != nil || len(entries) == 0 {
		return ""
	}

	return entries[0].V4.Status
}

// jsonBody skips any vtysh warnings printed before the JSON payload,
// which starts at the given opening byte.
func jsonBody(out []byte, open byte) []byte {
	if i := bytes.IndexByte(out, open); i > 0 {
		return out[i:]
	}

	return out
}
