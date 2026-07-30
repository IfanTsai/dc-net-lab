package biz

import (
	"slices"
	"testing"
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
