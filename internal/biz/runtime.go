package biz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// NodeRuntime is the management view of one deployed node: the state
// of the underlying container and every kernel interface inside it,
// including implementation plumbing (bridge, VRRP macvlan, management
// eth0) that the simulated topology does not model.
type NodeRuntime struct {
	ContainerState string
	Interfaces     []RuntimeInterface
}

// RuntimeInterface is one kernel interface of a node's container.
type RuntimeInterface struct {
	Name      string
	State     string
	MAC       string
	Addresses []string
}

// runtimeScript lists all kernel interfaces with their state, MAC and
// addresses; the two brief tables are joined by name when parsed.
const runtimeScript = `ip -br link; echo __SEP__; ip -br addr`

// RuntimeUsecase serves the management (container-level) view of
// deployed nodes on demand; the periodic observer only carries the
// simulated slice of this data.
type RuntimeUsecase struct {
	labs   LabRepo
	nodes  TopologyRepo
	driver runtime.Driver
	log    *slog.Logger
}

// NewRuntimeUsecase wires the runtime usecase.
func NewRuntimeUsecase(labs LabRepo, nodes TopologyRepo, driver runtime.Driver, log *slog.Logger) *RuntimeUsecase {
	return &RuntimeUsecase{labs: labs, nodes: nodes, driver: driver, log: log}
}

// GetNodeRuntime returns the container-level state of one node of a
// deployed lab. Interfaces are only collected from a running
// container: exec would hang on a paused one.
func (uc *RuntimeUsecase) GetNodeRuntime(ctx context.Context, labID, nodeID string) (*NodeRuntime, error) {
	lab, node, state, err := uc.deployedNode(ctx, labID, nodeID)
	if err != nil {
		return nil, err
	}

	rt := &NodeRuntime{ContainerState: state}
	if state != "running" {
		return rt, nil
	}

	out, err := uc.driver.Exec(ctx, lab.Meta.Name, node.Meta.Name, []string{"sh", "-c", runtimeScript})
	if err != nil {
		return nil, fmt.Errorf("exec into %q: %w", node.Meta.Name, err)
	}

	rt.Interfaces = parseRuntimeInterfaces(out)

	return rt, nil
}

// NodeRoutes is the live IPv4 routing table of one deployed node.
type NodeRoutes struct {
	ContainerState string
	Routes         []Route
}

// Route is one IPv4 RIB entry as reported by FRR. The FIB view
// reuses it with Kind set for host ("local") entries.
type Route struct {
	Prefix   string
	Protocol string
	Kind     string
	Selected bool
	Distance int
	Metric   int
	Nexthops []RouteNexthop
}

// RouteNexthop is one next hop of a RIB entry; Via is empty for
// directly connected routes.
type RouteNexthop struct {
	Via       string
	Interface string
	Active    bool
}

// GetNodeRoutes returns the live FRR RIB of one node of a deployed
// lab; routes are only collected from a running container.
func (uc *RuntimeUsecase) GetNodeRoutes(ctx context.Context, labID, nodeID string) (*NodeRoutes, error) {
	lab, node, state, err := uc.deployedNode(ctx, labID, nodeID)
	if err != nil {
		return nil, err
	}

	rt := &NodeRoutes{ContainerState: state}
	if state != "running" {
		return rt, nil
	}

	out, err := uc.driver.Exec(ctx, lab.Meta.Name, node.Meta.Name, []string{"vtysh", "-c", "show ip route json"})
	if err != nil {
		return nil, fmt.Errorf("exec into %q: %w", node.Meta.Name, err)
	}

	rt.Routes = parseRoutes(out)

	return rt, nil
}

// NodeBGPTable is the live BGP Loc-RIB of one deployed node: every
// candidate path per prefix, before best-path selection reduces them
// to the single RIB entry.
type NodeBGPTable struct {
	ContainerState string
	RouterID       string
	LocalAS        uint32
	Paths          []BGPPath
}

// BGPPath is one candidate path of the BGP Loc-RIB.
type BGPPath struct {
	Prefix      string
	Best        bool
	Multipath   bool
	Valid       bool
	Internal    bool
	ASPath      string
	Origin      string
	LocalPref   uint32
	Peer        string
	Nexthop     string
	NexthopName string
}

// GetNodeBGPTable returns the live BGP Loc-RIB of one node of a
// deployed lab; paths are only collected from a running container.
func (uc *RuntimeUsecase) GetNodeBGPTable(ctx context.Context, labID, nodeID string) (*NodeBGPTable, error) {
	lab, node, state, err := uc.deployedNode(ctx, labID, nodeID)
	if err != nil {
		return nil, err
	}

	table := &NodeBGPTable{ContainerState: state}
	if state != "running" {
		return table, nil
	}

	out, err := uc.driver.Exec(ctx, lab.Meta.Name, node.Meta.Name, []string{"vtysh", "-c", "show ip bgp json"})
	if err != nil {
		return nil, fmt.Errorf("exec into %q: %w", node.Meta.Name, err)
	}

	table.RouterID, table.LocalAS, table.Paths = parseBGPTable(out)

	return table, nil
}

// GetNodeFIB returns the live kernel forwarding table of one node of
// a deployed lab; entries are only collected from a running
// container. Like a real switch FIB it covers both layers: the main
// table (LPM forwarding entries) and the local table's host entries
// (traffic addressed to the device itself, the ASIC host/punt table
// equivalent).
func (uc *RuntimeUsecase) GetNodeFIB(ctx context.Context, labID, nodeID string) (*NodeRoutes, error) {
	lab, node, state, err := uc.deployedNode(ctx, labID, nodeID)
	if err != nil {
		return nil, err
	}

	rt := &NodeRoutes{ContainerState: state}
	if state != "running" {
		return rt, nil
	}

	out, err := uc.driver.Exec(ctx, lab.Meta.Name, node.Meta.Name, []string{"ip", "-4", "-j", "route", "show", "table", "all"})
	if err != nil {
		return nil, fmt.Errorf("exec into %q: %w", node.Meta.Name, err)
	}

	rt.Routes = parseFIB(out)

	return rt, nil
}

// deployedNode resolves one node of a deployed lab together with its
// live container state.
func (uc *RuntimeUsecase) deployedNode(ctx context.Context, labID, nodeID string) (*model.Lab, *model.Node, string, error) {
	lab, err := uc.labs.GetLab(labID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Generation == 0 {
		return nil, nil, "", fmt.Errorf("lab %q has no deployed generation; apply a plan first", lab.Meta.Name)
	}

	nodes, err := uc.nodes.ListNodes(labID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("list nodes: %w", err)
	}

	var node *model.Node
	for _, n := range nodes {
		if n.Meta.ID == nodeID {
			node = n

			break
		}
	}

	if node == nil {
		return nil, nil, "", fmt.Errorf("node %q: %w", nodeID, ErrNotFound)
	}

	states, err := uc.driver.NodeStates(ctx, lab.Meta.Name, []string{node.Meta.Name})
	if err != nil {
		return nil, nil, "", fmt.Errorf("node states: %w", err)
	}

	return lab, node, states[node.Meta.Name], nil
}

// parseRoutes reads `show ip route json` output: a map of prefix to
// RIB entries (usually one per prefix, more when several protocols
// know the destination). Entries come back sorted by prefix so the
// table is stable across refreshes.
func parseRoutes(out []byte) []Route {
	var table map[string][]struct {
		Protocol string `json:"protocol"`
		Selected bool   `json:"selected"`
		Distance int    `json:"distance"`
		Metric   int    `json:"metric"`
		Nexthops []struct {
			IP            string `json:"ip"`
			InterfaceName string `json:"interfaceName"`
			Active        bool   `json:"active"`
		} `json:"nexthops"`
	}

	if err := json.Unmarshal(jsonBody(out), &table); err != nil {
		return nil
	}

	var routes []Route
	for prefix, entries := range table {
		for _, e := range entries {
			r := Route{
				Prefix:   prefix,
				Protocol: e.Protocol,
				Selected: e.Selected,
				Distance: e.Distance,
				Metric:   e.Metric,
			}

			for _, nh := range e.Nexthops {
				r.Nexthops = append(r.Nexthops, RouteNexthop{
					Via:       nh.IP,
					Interface: nh.InterfaceName,
					Active:    nh.Active,
				})
			}

			routes = append(routes, r)
		}
	}

	slices.SortFunc(routes, func(a, b Route) int {
		return comparePrefixes(a.Prefix, b.Prefix)
	})

	return routes
}

// parseBGPTable reads `show ip bgp json` output: a routes map of
// prefix to candidate paths. Paths come back sorted by prefix, best
// path first within a prefix.
func parseBGPTable(out []byte) (routerID string, localAS uint32, paths []BGPPath) {
	var table struct {
		RouterID string `json:"routerId"`
		LocalAS  uint32 `json:"localAS"`
		Routes   map[string][]struct {
			Network   string `json:"network"`
			Bestpath  bool   `json:"bestpath"`
			Multipath bool   `json:"multipath"`
			Valid     bool   `json:"valid"`
			PathFrom  string `json:"pathFrom"`
			Path      string `json:"path"`
			Origin    string `json:"origin"`
			LocPrf    uint32 `json:"locPrf"`
			PeerID    string `json:"peerId"`
			Nexthops  []struct {
				IP       string `json:"ip"`
				Hostname string `json:"hostname"`
			} `json:"nexthops"`
		} `json:"routes"`
	}

	if err := json.Unmarshal(jsonBody(out), &table); err != nil {
		return "", 0, nil
	}

	for prefix, entries := range table.Routes {
		for _, e := range entries {
			p := BGPPath{
				Prefix:    prefix,
				Best:      e.Bestpath,
				Multipath: e.Multipath,
				Valid:     e.Valid,
				Internal:  e.PathFrom == "internal",
				ASPath:    e.Path,
				Origin:    e.Origin,
				LocalPref: e.LocPrf,
				Peer:      e.PeerID,
			}

			if len(e.Nexthops) > 0 {
				p.Nexthop = e.Nexthops[0].IP
				p.NexthopName = e.Nexthops[0].Hostname
			}

			paths = append(paths, p)
		}
	}

	slices.SortFunc(paths, func(a, b BGPPath) int {
		if c := comparePrefixes(a.Prefix, b.Prefix); c != 0 {
			return c
		}

		switch {
		case a.Best != b.Best && a.Best:
			return -1
		case a.Best != b.Best:
			return 1
		}

		return strings.Compare(a.Peer, b.Peer)
	})

	return table.RouterID, table.LocalAS, paths
}

// parseFIB reads `ip -4 -j route show table all` output: a flat
// array where an ECMP route carries a nexthops list and a
// single-path route has gateway/dev at the top level. Unicast (main
// table LPM) and local (host) entries are kept; broadcast/multicast
// plumbing and the 127/8 loopback are implementation noise a switch
// FIB view would not show. IPv6 entries are excluded on top of the
// -4 flag: their dst is also "default"/"ff00::/8" style and the
// default route would otherwise masquerade as 0.0.0.0/0 after
// normalisation. dst is normalised to CIDR ("default" and bare host
// addresses included) so the table sorts like the RIB.
func parseFIB(out []byte) []Route {
	var entries []struct {
		Type     string `json:"type"`
		Dst      string `json:"dst"`
		Gateway  string `json:"gateway"`
		Dev      string `json:"dev"`
		Protocol string `json:"protocol"`
		Metric   int    `json:"metric"`
		Nexthops []struct {
			Gateway string `json:"gateway"`
			Dev     string `json:"dev"`
		} `json:"nexthops"`
	}

	if err := json.Unmarshal(out, &entries); err != nil {
		return nil
	}

	var routes []Route
	for _, e := range entries {
		if e.Type != "" && e.Type != "unicast" && e.Type != "local" {
			continue
		}

		if strings.Contains(e.Gateway, ":") {
			continue // IPv6 route hiding behind a "default" dst
		}

		prefix := normalizeDst(e.Dst)
		if p, err := netip.ParsePrefix(prefix); err != nil || !p.Addr().Is4() || p.Addr().IsLoopback() {
			continue
		}

		r := Route{
			Prefix:   prefix,
			Protocol: e.Protocol,
			Selected: true, // everything in the FIB forwards
			Metric:   e.Metric,
		}

		// iproute2 omits the protocol for manually added routes
		// (RTPROT_BOOT, e.g. the server default set via `ip route
		// replace`); name it explicitly instead of an empty cell.
		if r.Protocol == "" {
			r.Protocol = "boot"
		}

		if e.Type == "local" {
			r.Kind = "local"
		}

		for _, nh := range e.Nexthops {
			r.Nexthops = append(r.Nexthops, RouteNexthop{Via: nh.Gateway, Interface: nh.Dev, Active: true})
		}

		if len(e.Nexthops) == 0 {
			r.Nexthops = []RouteNexthop{{Via: e.Gateway, Interface: e.Dev, Active: true}}
		}

		routes = append(routes, r)
	}

	slices.SortFunc(routes, func(a, b Route) int {
		if c := comparePrefixes(a.Prefix, b.Prefix); c != 0 {
			return c
		}

		return strings.Compare(a.Kind, b.Kind) // forwarding entry before host entry
	})

	return routes
}

// normalizeDst turns iproute2 destinations into CIDR: "default" is
// the default route and a bare address is a host route.
func normalizeDst(dst string) string {
	switch {
	case dst == "default":
		return "0.0.0.0/0"
	case !strings.Contains(dst, "/"):
		return dst + "/32"
	}

	return dst
}

// comparePrefixes orders CIDR strings by address then prefix length;
// unparseable strings sort last.
func comparePrefixes(a, b string) int {
	pa, errA := netip.ParsePrefix(a)
	pb, errB := netip.ParsePrefix(b)
	if errA != nil || errB != nil {
		switch {
		case errA == nil:
			return -1
		case errB == nil:
			return 1
		}

		return strings.Compare(a, b)
	}

	if c := pa.Addr().Compare(pb.Addr()); c != 0 {
		return c
	}

	return pa.Bits() - pb.Bits()
}

// jsonBody skips any vtysh warnings printed before the JSON payload.
func jsonBody(out []byte) []byte {
	if i := bytes.IndexByte(out, '{'); i > 0 {
		return out[i:]
	}

	return out
}

// parseRuntimeInterfaces joins the `ip -br link` and `ip -br addr`
// tables by interface name. Link lines look like
// "eth1@if21  UP  aa:c1:ab:00:00:01 <BROADCAST,...>", address lines
// like "eth1  UP  10.0.1.2/31 fe80::1/64".
func parseRuntimeInterfaces(out []byte) []RuntimeInterface {
	linkPart, addrPart, _ := strings.Cut(string(out), "__SEP__")

	addrs := make(map[string][]string)
	for _, line := range strings.Split(addrPart, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		name, _, _ := strings.Cut(fields[0], "@")
		addrs[name] = fields[2:]
	}

	var ifaces []RuntimeInterface
	for _, line := range strings.Split(linkPart, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		name, _, _ := strings.Cut(fields[0], "@")
		iface := RuntimeInterface{Name: name, State: fields[1], Addresses: addrs[name]}
		if len(fields) > 2 {
			iface.MAC = fields[2]
		}

		ifaces = append(ifaces, iface)
	}

	return ifaces
}
