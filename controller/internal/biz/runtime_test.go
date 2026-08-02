package biz

import (
	"context"
	"net/netip"
	"slices"
	"strconv"
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

func TestParseRuntimeInterfaces(t *testing.T) {
	out := []byte(`lo               UNKNOWN        00:00:00:00:00:00 <LOOPBACK,UP,LOWER_UP>
eth0@if20        UP             02:42:ac:14:14:09 <BROADCAST,MULTICAST,UP,LOWER_UP>
eth1@if21        UP             aa:c1:ab:00:00:01 <BROADCAST,MULTICAST,UP,LOWER_UP>
vrrp4-1-1@vlan1000 DOWN         00:00:5e:00:01:01 <BROADCAST,MULTICAST>
__SEP__
lo               UNKNOWN        127.0.0.1/8
eth0@if20        UP             172.20.20.3/24 fe80::42:acff:fe14:1409/64
eth1@if21        UP             10.0.1.2/31
vrrp4-1-1@vlan1000 DOWN         10.100.0.1/32
`)

	got := parseRuntimeInterfaces(out)

	want := []RuntimeInterface{
		{Name: "lo", State: "UNKNOWN", MAC: "00:00:00:00:00:00", Addresses: []string{"127.0.0.1/8"}},
		{Name: "eth0", State: "UP", MAC: "02:42:ac:14:14:09", Addresses: []string{"172.20.20.3/24", "fe80::42:acff:fe14:1409/64"}},
		{Name: "eth1", State: "UP", MAC: "aa:c1:ab:00:00:01", Addresses: []string{"10.0.1.2/31"}},
		{Name: "vrrp4-1-1", State: "DOWN", MAC: "00:00:5e:00:01:01", Addresses: []string{"10.100.0.1/32"}},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d interfaces, want %d: %+v", len(got), len(want), got)
	}

	for i, w := range want {
		g := got[i]
		if g.Name != w.Name || g.State != w.State || g.MAC != w.MAC || !slices.Equal(g.Addresses, w.Addresses) {
			t.Errorf("interface %d: got %+v, want %+v", i, g, w)
		}
	}
}

func TestParseRuntimeInterfacesEmpty(t *testing.T) {
	if got := parseRuntimeInterfaces(nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestParseRoutes(t *testing.T) {
	// Trimmed `show ip route json` output: an ECMP BGP route, a
	// connected route and a default route, deliberately out of order
	// (JSON maps are unordered anyway).
	out := []byte(`{
		"10.0.0.0/31": [{
			"protocol": "bgp", "selected": true, "distance": 20, "metric": 0,
			"nexthops": [
				{"ip": "10.0.0.22", "interfaceName": "eth1", "active": true},
				{"ip": "10.0.0.26", "interfaceName": "eth2", "active": true}
			]
		}],
		"0.0.0.0/0": [{
			"protocol": "bgp", "selected": true, "distance": 20, "metric": 0,
			"nexthops": [{"ip": "10.100.0.1", "interfaceName": "bond0", "active": true}]
		}],
		"10.100.0.0/24": [{
			"protocol": "connected", "selected": true, "distance": 0, "metric": 0,
			"nexthops": [{"interfaceName": "vlan1000", "active": true}]
		}]
	}`)

	got := parseRoutes(out)

	wantPrefixes := []string{"0.0.0.0/0", "10.0.0.0/31", "10.100.0.0/24"}
	if len(got) != len(wantPrefixes) {
		t.Fatalf("got %d routes, want %d: %+v", len(got), len(wantPrefixes), got)
	}

	for i, p := range wantPrefixes {
		if got[i].Prefix != p {
			t.Errorf("route %d: got prefix %s, want %s", i, got[i].Prefix, p)
		}
	}

	ecmp := got[1]
	if ecmp.Protocol != "bgp" || ecmp.Distance != 20 || len(ecmp.Nexthops) != 2 {
		t.Errorf("ecmp route: %+v", ecmp)
	}

	if nh := ecmp.Nexthops[0]; nh.Via != "10.0.0.22" || nh.Interface != "eth1" || !nh.Active {
		t.Errorf("nexthop: %+v", nh)
	}

	connected := got[2]
	if connected.Protocol != "connected" || connected.Nexthops[0].Via != "" {
		t.Errorf("connected route: %+v", connected)
	}

	if parseRoutes([]byte("% garbage")) != nil {
		t.Error("garbage input should yield no routes")
	}
}

func TestParseBGPTable(t *testing.T) {
	// Trimmed `show ip bgp json`: one prefix with an eBGP best path,
	// an eBGP multipath candidate and an iBGP path from the MLAG peer.
	out := []byte(`{
		"routerId": "10.1.0.8", "localAS": 4200080001,
		"routes": {
			"10.1.0.1/32": [
				{
					"network": "10.1.0.1/32", "multipath": true, "valid": true,
					"pathFrom": "external", "path": "65201 65101 64600", "origin": "incomplete",
					"peerId": "10.0.0.26",
					"nexthops": [{"ip": "10.0.0.26", "hostname": "pod-1-spine-2"}]
				},
				{
					"network": "10.1.0.1/32", "bestpath": true, "multipath": true, "valid": true,
					"pathFrom": "external", "path": "65200 65101 64600", "origin": "incomplete",
					"peerId": "10.0.0.22",
					"nexthops": [{"ip": "10.0.0.22", "hostname": "pod-1-spine-1"}]
				},
				{
					"network": "10.1.0.1/32", "valid": true, "locPrf": 100,
					"pathFrom": "internal", "path": "65200 65101 64600", "origin": "incomplete",
					"peerId": "10.100.0.2",
					"nexthops": [{"ip": "10.0.0.20", "hostname": "pod-1-rack-1-leaf-a"}]
				}
			]
		}
	}`)

	routerID, localAS, paths := parseBGPTable(out)

	if routerID != "10.1.0.8" || localAS != 4200080001 {
		t.Errorf("header: routerID=%s localAS=%d", routerID, localAS)
	}

	if len(paths) != 3 {
		t.Fatalf("got %d paths, want 3: %+v", len(paths), paths)
	}

	best := paths[0]
	if !best.Best || best.Peer != "10.0.0.22" || best.NexthopName != "pod-1-spine-1" {
		t.Errorf("best path must sort first: %+v", best)
	}

	var ibgp *BGPPath
	for i := range paths {
		if paths[i].Internal {
			ibgp = &paths[i]
		}
	}

	if ibgp == nil || ibgp.Best || ibgp.LocalPref != 100 || ibgp.Nexthop != "10.0.0.20" {
		t.Errorf("iBGP path: %+v", ibgp)
	}

	if _, _, got := parseBGPTable([]byte("% garbage")); got != nil {
		t.Error("garbage input should yield no paths")
	}
}

func TestParseFIB(t *testing.T) {
	// Trimmed `ip -j route show table all`: an ECMP BGP route, a
	// single-nexthop default route, a connected subnet, a loopback
	// host entry from the local table, plus broadcast/multicast/IPv6
	// and 127/8 plumbing that a switch FIB view would not show.
	out := []byte(`[
		{"dst": "10.0.0.0/31", "protocol": "bgp", "metric": 20, "nexthops": [
			{"gateway": "10.0.0.22", "dev": "eth1", "weight": 1},
			{"gateway": "10.0.0.26", "dev": "eth2", "weight": 1}
		]},
		{"dst": "default", "gateway": "10.100.0.1", "dev": "bond0", "metric": 20},
		{"dst": "10.100.0.0/24", "dev": "vlan1000", "protocol": "kernel"},
		{"type": "local", "dst": "10.1.0.8", "dev": "lo", "table": "local", "protocol": "kernel"},
		{"type": "broadcast", "dst": "10.100.0.255", "dev": "vlan1000", "table": "local", "protocol": "kernel"},
		{"type": "local", "dst": "127.0.0.0/8", "dev": "lo", "table": "local", "protocol": "kernel"},
		{"type": "multicast", "dst": "ff00::/8", "dev": "eth0", "table": "local", "protocol": "kernel"},
		{"dst": "default", "gateway": "3fff:172:20:20::1", "dev": "eth0", "metric": 1024}
	]`)

	got := parseFIB(out)

	wantPrefixes := []string{"0.0.0.0/0", "10.0.0.0/31", "10.1.0.8/32", "10.100.0.0/24"}
	if len(got) != len(wantPrefixes) {
		t.Fatalf("got %d routes, want %d: %+v", len(got), len(wantPrefixes), got)
	}

	for i, p := range wantPrefixes {
		if got[i].Prefix != p {
			t.Errorf("route %d: got prefix %s, want %s", i, got[i].Prefix, p)
		}
	}

	// A manually added route comes back without a protocol field and
	// must be named boot, not left blank.
	if def := got[0]; def.Kind != "" || def.Protocol != "boot" || def.Nexthops[0].Via != "10.100.0.1" {
		t.Errorf("default route: %+v", def)
	}

	if ecmp := got[1]; len(ecmp.Nexthops) != 2 || ecmp.Nexthops[1].Interface != "eth2" {
		t.Errorf("ecmp nexthops: %+v", ecmp.Nexthops)
	}

	if lo := got[2]; lo.Kind != "local" || lo.Nexthops[0].Interface != "lo" {
		t.Errorf("loopback host entry: %+v", lo)
	}

	if parseFIB([]byte("garbage")) != nil {
		t.Error("garbage input should yield no routes")
	}
}

func TestMTRArgs(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		port     int
		want     []string
	}{
		{"icmp", MTRProtocolICMP, 0, []string{"mtr", "--report", "--json", "-4", "-n", "-c", "10", "10.0.0.1"}},
		{"tcp", MTRProtocolTCP, 443, []string{"mtr", "--report", "--json", "-4", "-n", "-c", "10", "-T", "-P", "443", "10.0.0.1"}},
		{"udp", MTRProtocolUDP, 53, []string{"mtr", "--report", "--json", "-4", "-n", "-c", "10", "-u", "-P", "53", "10.0.0.1"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mtrArgs("10.0.0.1", c.protocol, c.port, 10)
			if !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseMTRHops(t *testing.T) {
	// Trimmed `mtr --report --json -n` output: one hop resolving to a
	// known node, one timing out, one reaching the target.
	out := []byte(`{
		"report": {
			"mtr": {"src": "10.1.0.10", "dst": "10.1.0.30", "tests": 10},
			"hubs": [
				{"count": 0, "host": "10.0.0.1", "Loss%": 0.0, "Snt": 10, "Last": 0.4, "Avg": 0.5, "Best": 0.3, "Wrst": 0.9, "StDev": 0.1},
				{"count": 1, "host": "???", "Loss%": 100.0, "Snt": 10, "Last": 0.0, "Avg": 0.0, "Best": 0.0, "Wrst": 0.0, "StDev": 0.0},
				{"count": 2, "host": "10.1.0.30", "Loss%": 0.0, "Snt": 10, "Last": 1.1, "Avg": 1.2, "Best": 1.0, "Wrst": 1.5, "StDev": 0.2}
			]
		}
	}`)

	idx := &mtrNodeIndex{
		byAddr: map[string]*model.Node{
			"10.0.0.1":  {Meta: model.ResourceMeta{ID: "n-spine", Name: "spine-1"}, Spec: model.NodeSpec{Role: model.RoleSpine}},
			"10.1.0.30": {Meta: model.ResourceMeta{ID: "n-leaf-b", Name: "leaf-b"}, Spec: model.NodeSpec{Role: model.RoleLeaf}},
		},
		byPair: map[string]string{},
	}

	hops := parseMTRHops(out, idx)
	if len(hops) != 3 {
		t.Fatalf("got %d hops, want 3: %+v", len(hops), hops)
	}

	if h := hops[0]; h.TTL != 1 || h.Timeout || h.NodeID != "n-spine" || h.NodeName != "spine-1" || h.NodeRole != string(model.RoleSpine) || h.AvgMs != 0.5 {
		t.Errorf("hop 1: %+v", h)
	}

	if h := hops[1]; !h.Timeout || h.NodeID != "" || h.LossPercent != 100 {
		t.Errorf("hop 2 (timeout): %+v", h)
	}

	if h := hops[2]; h.Timeout || h.NodeID != "n-leaf-b" || h.WorstMs != 1.5 {
		t.Errorf("hop 3: %+v", h)
	}

	if parseMTRHops([]byte("garbage"), idx) != nil {
		t.Error("garbage input should yield no hops")
	}
}

func TestBuildMTRIndexAndPath(t *testing.T) {
	spine := &model.Node{
		Meta: model.ResourceMeta{ID: "n-spine", Name: "spine-1"},
		Spec: model.NodeSpec{Role: model.RoleSpine, RuntimeType: model.RuntimeFRR, Loopback: netip.MustParsePrefix("10.1.0.20/32")},
	}

	leafA := &model.Node{
		Meta: model.ResourceMeta{ID: "n-leaf-a", Name: "leaf-a"},
		Spec: model.NodeSpec{
			Role: model.RoleLeaf, RuntimeType: model.RuntimeFRR,
			Loopback: netip.MustParsePrefix("10.1.0.10/32"),
			// A server's first hop out replies from the leaf's VLAN SVI,
			// not its loopback — must resolve too (real-environment gap
			// found probing from pod-1-rack-1-server-1: the leaf hop
			// came back as 10.100.0.2, unresolved until this was added).
			VlanIP: netip.MustParsePrefix("10.100.0.2/24"),
		},
	}

	leafB := &model.Node{
		Meta: model.ResourceMeta{ID: "n-leaf-b", Name: "leaf-b"},
		Spec: model.NodeSpec{Role: model.RoleLeaf, RuntimeType: model.RuntimeFRR, Loopback: netip.MustParsePrefix("10.1.0.30/32")},
	}

	nodes := []*model.Node{spine, leafA, leafB}

	link1 := &model.Link{
		Meta: model.ResourceMeta{ID: "l-1"},
		Spec: model.LinkSpec{
			Kind:      model.LinkFabric,
			EndpointA: model.LinkEndpoint{NodeID: leafA.Meta.ID, Address: netip.MustParsePrefix("10.0.0.0/31")},
			EndpointB: model.LinkEndpoint{NodeID: spine.Meta.ID, Address: netip.MustParsePrefix("10.0.0.1/31")},
		},
	}

	link2 := &model.Link{
		Meta: model.ResourceMeta{ID: "l-2"},
		Spec: model.LinkSpec{
			Kind:      model.LinkFabric,
			EndpointA: model.LinkEndpoint{NodeID: spine.Meta.ID, Address: netip.MustParsePrefix("10.0.0.2/31")},
			EndpointB: model.LinkEndpoint{NodeID: leafB.Meta.ID, Address: netip.MustParsePrefix("10.0.0.3/31")},
		},
	}

	links := []*model.Link{link1, link2}

	idx := buildMTRIndex(nodes, links)

	for addr, wantID := range map[string]string{
		"10.1.0.20":  spine.Meta.ID,
		"10.1.0.10":  leafA.Meta.ID,
		"10.100.0.2": leafA.Meta.ID,
		"10.0.0.0":   leafA.Meta.ID,
		"10.0.0.1":   spine.Meta.ID,
		"10.0.0.3":   leafB.Meta.ID,
	} {
		n, ok := idx.byAddr[addr]
		if !ok || n.Meta.ID != wantID {
			t.Errorf("byAddr[%s] = %v, want %s", addr, n, wantID)
		}
	}

	if id, ok := idx.byPair[pairKey(spine.Meta.ID, leafB.Meta.ID)]; !ok || id != link2.Meta.ID {
		t.Errorf("byPair(spine,leafB) = %s, want %s", id, link2.Meta.ID)
	}

	// mtr never lists the probing device itself as a hop, so the path
	// must be seeded from it: leaf-a -[l-1]-> spine -> (unresolved
	// timeout) -[l-2]-> leaf-b must highlight both links and skip
	// cleanly over the gap.
	hops := []MTRHop{
		{NodeID: spine.Meta.ID},
		{Timeout: true},
		{NodeID: leafB.Meta.ID},
	}

	gotPath := mtrPathLinkIDs(leafA.Meta.ID, hops, idx)
	wantPath := []string{link1.Meta.ID, link2.Meta.ID}
	if !slices.Equal(gotPath, wantPath) {
		t.Errorf("path = %v, want %v", gotPath, wantPath)
	}
}

func TestMTRNodeAddress(t *testing.T) {
	router := &model.Node{Spec: model.NodeSpec{RuntimeType: model.RuntimeFRR, Loopback: netip.MustParsePrefix("10.1.0.1/32")}}
	if addr, ok := mtrNodeAddress(router); !ok || addr.String() != "10.1.0.1" {
		t.Errorf("router address: %v, %v", addr, ok)
	}

	server := &model.Node{Spec: model.NodeSpec{Role: model.RoleServer, Address: netip.MustParsePrefix("10.100.0.11/24")}}
	if addr, ok := mtrNodeAddress(server); !ok || addr.String() != "10.100.0.11" {
		t.Errorf("server address: %v, %v", addr, ok)
	}

	if _, ok := mtrNodeAddress(&model.Node{}); ok {
		t.Error("bare node should have no resolvable address")
	}
}

// fakeMTRRepo satisfies both LabRepo and TopologyRepo with an
// in-memory fixture; methods the RunMTR path never touches are
// no-ops.
type fakeMTRRepo struct {
	lab   *model.Lab
	nodes []*model.Node
	links []*model.Link
}

func (r *fakeMTRRepo) CreateLab(*model.Lab) error                         { return nil }
func (r *fakeMTRRepo) UpdateLab(*model.Lab) error                         { return nil }
func (r *fakeMTRRepo) ListLabs() ([]*model.Lab, error)                    { return nil, nil }
func (r *fakeMTRRepo) DeleteLab(string) error                             { return nil }
func (r *fakeMTRRepo) ListNodes(string) ([]*model.Node, error)            { return r.nodes, nil }
func (r *fakeMTRRepo) ListLinks(string) ([]*model.Link, error)            { return r.links, nil }
func (r *fakeMTRRepo) ListAllocations(string) ([]model.Allocation, error) { return nil, nil }

func (r *fakeMTRRepo) GetLab(id string) (*model.Lab, error) {
	if r.lab == nil || r.lab.Meta.ID != id {
		return nil, ErrNotFound
	}

	return r.lab, nil
}

// fakeMTRDriver runs the probing node as always up and returns a
// canned mtr JSON report, recording the exact argv it was asked to
// exec so tests can assert on protocol/port/cycles translation.
type fakeMTRDriver struct {
	runtime.Driver

	report    []byte
	reports   [][]byte // when set, cycled through across successive Exec calls instead of report
	execErr   error
	lastCmd   []string
	execCount int
	state     string // defaults to "running" when empty
}

func (d *fakeMTRDriver) NodeStates(ctx context.Context, labName string, names []string) (map[string]string, error) {
	state := d.state
	if state == "" {
		state = "running"
	}

	states := make(map[string]string, len(names))
	for _, n := range names {
		states[n] = state
	}

	return states, nil
}

func (d *fakeMTRDriver) Exec(ctx context.Context, labName, nodeName string, cmd []string) ([]byte, error) {
	d.lastCmd = cmd
	d.execCount++

	if d.execErr != nil {
		return nil, d.execErr
	}

	if len(d.reports) > 0 {
		return d.reports[(d.execCount-1)%len(d.reports)], nil
	}

	return d.report, nil
}

func setupMTR(t *testing.T) (*RuntimeUsecase, *fakeMTRDriver, *model.Node, *model.Node) {
	t.Helper()

	lab := &model.Lab{Meta: model.ResourceMeta{ID: "lab-1", Name: "lab-1", Generation: 1}}
	probe := &model.Node{
		Meta: model.ResourceMeta{ID: "n-probe", Name: "leaf-a"},
		Spec: model.NodeSpec{Role: model.RoleLeaf, RuntimeType: model.RuntimeFRR, Loopback: netip.MustParsePrefix("10.1.0.10/32")},
	}

	target := &model.Node{
		Meta: model.ResourceMeta{ID: "n-target", Name: "leaf-b"},
		Spec: model.NodeSpec{Role: model.RoleLeaf, RuntimeType: model.RuntimeFRR, Loopback: netip.MustParsePrefix("10.1.0.30/32")},
	}

	repo := &fakeMTRRepo{lab: lab, nodes: []*model.Node{probe, target}}
	driver := &fakeMTRDriver{report: []byte(`{"report":{"hubs":[]}}`)}
	uc := NewRuntimeUsecase(repo, repo, driver, testLog())

	return uc, driver, probe, target
}

func TestRunMTRValidation(t *testing.T) {
	uc, _, probe, _ := setupMTR(t)
	ctx := context.Background()

	if _, err := uc.RunMTR(ctx, "lab-1", probe.Meta.ID, "", "8.8.8.8", "carrier-pigeon", 0, 10); err == nil {
		t.Error("invalid protocol must be rejected")
	}

	if _, err := uc.RunMTR(ctx, "lab-1", probe.Meta.ID, "", "8.8.8.8", MTRProtocolTCP, 0, 10); err == nil {
		t.Error("tcp probe without a port must be rejected")
	}

	if _, err := uc.RunMTR(ctx, "lab-1", probe.Meta.ID, "", "", "", 0, 10); err == nil {
		t.Error("empty target must be rejected")
	}
}

func TestRunMTRResolvesTargetNode(t *testing.T) {
	uc, driver, probe, target := setupMTR(t)

	result, err := uc.RunMTR(context.Background(), "lab-1", probe.Meta.ID, target.Meta.ID, "", MTRProtocolICMP, 0, 999)
	if err != nil {
		t.Fatalf("RunMTR: %v", err)
	}

	if result.Target != "10.1.0.30" {
		t.Errorf("target = %q, want the resolved node's loopback", result.Target)
	}

	// cycles must be clamped to the cap, and the resolved target must
	// be the final argv element.
	if !slices.Contains(driver.lastCmd, "-c") {
		t.Fatalf("exec argv missing -c: %v", driver.lastCmd)
	}

	if got := driver.lastCmd[len(driver.lastCmd)-1]; got != "10.1.0.30" {
		t.Errorf("exec argv target = %q, want 10.1.0.30", got)
	}

	found := false
	for i, a := range driver.lastCmd {
		if a == "-c" && i+1 < len(driver.lastCmd) {
			found = a != "" && driver.lastCmd[i+1] == "30"
		}
	}

	if !found {
		t.Errorf("cycles not clamped to 30: %v", driver.lastCmd)
	}
}

func TestRunMTREndToEndPathResolution(t *testing.T) {
	uc, driver, probe, target := setupMTR(t)

	spine := &model.Node{
		Meta: model.ResourceMeta{ID: "n-spine", Name: "spine-1"},
		Spec: model.NodeSpec{Role: model.RoleSpine, RuntimeType: model.RuntimeFRR, Loopback: netip.MustParsePrefix("10.1.0.20/32")},
	}

	repo := uc.nodes.(*fakeMTRRepo)
	repo.nodes = append(repo.nodes, spine)
	repo.links = []*model.Link{
		{
			Meta: model.ResourceMeta{ID: "l-1"},
			Spec: model.LinkSpec{
				Kind:      model.LinkFabric,
				EndpointA: model.LinkEndpoint{NodeID: probe.Meta.ID, Address: netip.MustParsePrefix("10.0.0.0/31")},
				EndpointB: model.LinkEndpoint{NodeID: spine.Meta.ID, Address: netip.MustParsePrefix("10.0.0.1/31")},
			},
		},
		{
			Meta: model.ResourceMeta{ID: "l-2"},
			Spec: model.LinkSpec{
				Kind:      model.LinkFabric,
				EndpointA: model.LinkEndpoint{NodeID: spine.Meta.ID, Address: netip.MustParsePrefix("10.0.0.2/31")},
				EndpointB: model.LinkEndpoint{NodeID: target.Meta.ID, Address: netip.MustParsePrefix("10.0.0.3/31")},
			},
		},
	}

	driver.report = []byte(`{
		"report": {
			"hubs": [
				{"host": "10.0.0.1", "Loss%": 0.0, "Snt": 10, "Last": 0.4, "Avg": 0.5, "Best": 0.3, "Wrst": 0.9, "StDev": 0.1},
				{"host": "10.1.0.30", "Loss%": 0.0, "Snt": 10, "Last": 1.1, "Avg": 1.2, "Best": 1.0, "Wrst": 1.5, "StDev": 0.2}
			]
		}
	}`)

	result, err := uc.RunMTR(context.Background(), "lab-1", probe.Meta.ID, target.Meta.ID, "", MTRProtocolICMP, 0, 10)
	if err != nil {
		t.Fatalf("RunMTR: %v", err)
	}

	if len(result.Hops) != 2 || result.Hops[0].NodeID != spine.Meta.ID || result.Hops[1].NodeID != target.Meta.ID {
		t.Fatalf("hops not resolved: %+v", result.Hops)
	}

	wantPath := []string{"l-1", "l-2"}
	if !slices.Equal(result.PathLinkIDs, wantPath) {
		t.Errorf("path = %v, want %v", result.PathLinkIDs, wantPath)
	}
}

func TestRunMTRNotRunning(t *testing.T) {
	uc, driver, probe, _ := setupMTR(t)
	driver.state = "paused"

	result, err := uc.RunMTR(context.Background(), "lab-1", probe.Meta.ID, "", "8.8.8.8", MTRProtocolICMP, 0, 10)
	if err != nil {
		t.Fatalf("RunMTR: %v", err)
	}

	if result.ContainerState != "paused" || result.Hops != nil {
		t.Errorf("paused node must short-circuit before exec: %+v", result)
	}

	if driver.lastCmd != nil {
		t.Error("paused node must not be exec'd into")
	}
}

func TestRunMTRScanValidation(t *testing.T) {
	uc, _, probe, target := setupMTR(t)
	ctx := context.Background()

	if _, err := uc.RunMTRScan(ctx, "lab-1", probe.Meta.ID, target.Meta.ID, "", MTRProtocolICMP, 0, 4, 3); err == nil {
		t.Error("icmp scan must be rejected: it carries no port to vary the ECMP hash")
	}

	if _, err := uc.RunMTRScan(ctx, "lab-1", probe.Meta.ID, target.Meta.ID, "", MTRProtocolTCP, 0, 4, 3); err == nil {
		t.Error("tcp scan without a port must be rejected")
	}

	if _, err := uc.RunMTRScan(ctx, "lab-1", probe.Meta.ID, "", "", MTRProtocolUDP, 53, 4, 3); err == nil {
		t.Error("empty target must be rejected")
	}
}

func TestRunMTRScanNotRunning(t *testing.T) {
	uc, driver, probe, target := setupMTR(t)
	driver.state = "paused"

	result, err := uc.RunMTRScan(context.Background(), "lab-1", probe.Meta.ID, target.Meta.ID, "", MTRProtocolTCP, 80, 4, 3)
	if err != nil {
		t.Fatalf("RunMTRScan: %v", err)
	}

	if result.ContainerState != "paused" || result.Paths != nil || result.SamplesRun != 0 {
		t.Errorf("paused node must short-circuit before exec: %+v", result)
	}

	if driver.execCount != 0 {
		t.Error("paused node must not be exec'd into")
	}
}

func TestRunMTRScanClampsSamplesAndCycles(t *testing.T) {
	uc, driver, probe, target := setupMTR(t)

	result, err := uc.RunMTRScan(context.Background(), "lab-1", probe.Meta.ID, target.Meta.ID, "", MTRProtocolUDP, 9000, 999, 999)
	if err != nil {
		t.Fatalf("RunMTRScan: %v", err)
	}

	if result.SamplesRun != maxMTRScanSamples {
		t.Errorf("samples run = %d, want clamped %d", result.SamplesRun, maxMTRScanSamples)
	}

	found := false
	for i, a := range driver.lastCmd {
		if a == "-c" && i+1 < len(driver.lastCmd) {
			found = driver.lastCmd[i+1] == strconv.Itoa(maxMTRScanCycles)
		}
	}

	if !found {
		t.Errorf("cycles not clamped to %d: %v", maxMTRScanCycles, driver.lastCmd)
	}
}

// TestRunMTRScanGroupsDistinctPaths builds two ECMP branches (probe ->
// spine-1 -> target and probe -> spine-2 -> target) and drives the
// fake exec through a mixed sequence of both, asserting the scan
// groups samples by the path they actually measured rather than just
// counting runs.
func TestRunMTRScanGroupsDistinctPaths(t *testing.T) {
	uc, driver, probe, target := setupMTR(t)

	spine1 := &model.Node{
		Meta: model.ResourceMeta{ID: "n-spine-1", Name: "spine-1"},
		Spec: model.NodeSpec{Role: model.RoleSpine, RuntimeType: model.RuntimeFRR, Loopback: netip.MustParsePrefix("10.1.0.20/32")},
	}

	spine2 := &model.Node{
		Meta: model.ResourceMeta{ID: "n-spine-2", Name: "spine-2"},
		Spec: model.NodeSpec{Role: model.RoleSpine, RuntimeType: model.RuntimeFRR, Loopback: netip.MustParsePrefix("10.1.0.21/32")},
	}

	repo := uc.nodes.(*fakeMTRRepo)
	repo.nodes = append(repo.nodes, spine1, spine2)
	repo.links = []*model.Link{
		{
			Meta: model.ResourceMeta{ID: "l-probe-spine1"},
			Spec: model.LinkSpec{
				Kind:      model.LinkFabric,
				EndpointA: model.LinkEndpoint{NodeID: probe.Meta.ID, Address: netip.MustParsePrefix("10.0.1.0/31")},
				EndpointB: model.LinkEndpoint{NodeID: spine1.Meta.ID, Address: netip.MustParsePrefix("10.0.1.1/31")},
			},
		},
		{
			Meta: model.ResourceMeta{ID: "l-spine1-target"},
			Spec: model.LinkSpec{
				Kind:      model.LinkFabric,
				EndpointA: model.LinkEndpoint{NodeID: spine1.Meta.ID, Address: netip.MustParsePrefix("10.0.1.2/31")},
				EndpointB: model.LinkEndpoint{NodeID: target.Meta.ID, Address: netip.MustParsePrefix("10.0.1.3/31")},
			},
		},
		{
			Meta: model.ResourceMeta{ID: "l-probe-spine2"},
			Spec: model.LinkSpec{
				Kind:      model.LinkFabric,
				EndpointA: model.LinkEndpoint{NodeID: probe.Meta.ID, Address: netip.MustParsePrefix("10.0.2.0/31")},
				EndpointB: model.LinkEndpoint{NodeID: spine2.Meta.ID, Address: netip.MustParsePrefix("10.0.2.1/31")},
			},
		},
		{
			Meta: model.ResourceMeta{ID: "l-spine2-target"},
			Spec: model.LinkSpec{
				Kind:      model.LinkFabric,
				EndpointA: model.LinkEndpoint{NodeID: spine2.Meta.ID, Address: netip.MustParsePrefix("10.0.2.2/31")},
				EndpointB: model.LinkEndpoint{NodeID: target.Meta.ID, Address: netip.MustParsePrefix("10.0.2.3/31")},
			},
		},
	}

	reportViaSpine1 := []byte(`{"report":{"hubs":[
		{"host":"10.0.1.1","Loss%":0,"Snt":3,"Last":0.1,"Avg":0.1,"Best":0.1,"Wrst":0.1,"StDev":0},
		{"host":"10.1.0.30","Loss%":0,"Snt":3,"Last":0.1,"Avg":0.1,"Best":0.1,"Wrst":0.1,"StDev":0}
	]}}`)
	reportViaSpine2 := []byte(`{"report":{"hubs":[
		{"host":"10.0.2.1","Loss%":0,"Snt":3,"Last":0.1,"Avg":0.1,"Best":0.1,"Wrst":0.1,"StDev":0},
		{"host":"10.1.0.30","Loss%":0,"Snt":3,"Last":0.1,"Avg":0.1,"Best":0.1,"Wrst":0.1,"StDev":0}
	]}}`)
	driver.reports = [][]byte{reportViaSpine1, reportViaSpine1, reportViaSpine2, reportViaSpine1}

	result, err := uc.RunMTRScan(context.Background(), "lab-1", probe.Meta.ID, target.Meta.ID, "", MTRProtocolTCP, 8080, 4, 3)
	if err != nil {
		t.Fatalf("RunMTRScan: %v", err)
	}

	if result.SamplesRun != 4 {
		t.Fatalf("samples run = %d, want 4", result.SamplesRun)
	}

	if len(result.Paths) != 2 {
		t.Fatalf("got %d distinct paths, want 2: %+v", len(result.Paths), result.Paths)
	}

	if result.Paths[0].Count != 3 || !slices.Equal(result.Paths[0].PathLinkIDs, []string{"l-probe-spine1", "l-spine1-target"}) {
		t.Errorf("path 1 (spine-1, 3 samples): %+v", result.Paths[0])
	}

	if result.Paths[1].Count != 1 || !slices.Equal(result.Paths[1].PathLinkIDs, []string{"l-probe-spine2", "l-spine2-target"}) {
		t.Errorf("path 2 (spine-2, 1 sample): %+v", result.Paths[1])
	}
}
