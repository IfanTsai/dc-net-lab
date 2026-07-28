package observer

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
)

func TestSimulatedInterfaces(t *testing.T) {
	leaf := &model.Node{
		Meta: model.ResourceMeta{ID: "n-leaf", Name: "leaf-b"},
		Spec: model.NodeSpec{Role: model.RoleLeaf, VlanID: 1000},
	}

	server := &model.Node{
		Meta: model.ResourceMeta{ID: "n-srv", Name: "server-1"},
		Spec: model.NodeSpec{Role: model.RoleServer, Address: netip.MustParsePrefix("10.100.0.11/24")},
	}

	spine := &model.Node{
		Meta: model.ResourceMeta{ID: "n-spine", Name: "spine-1"},
		Spec: model.NodeSpec{Role: model.RoleSpine},
	}

	// Creation order is not per-node ascending; the result must be
	// numerically sorted (eth2 before eth10) with logical interfaces
	// last.
	links := []*model.Link{
		{Spec: model.LinkSpec{
			EndpointA: model.LinkEndpoint{NodeID: "n-leaf", Interface: "eth10"},
			EndpointB: model.LinkEndpoint{NodeID: "n-srv", Interface: "eth1"},
		}},
		{Spec: model.LinkSpec{
			EndpointA: model.LinkEndpoint{NodeID: "n-spine", Interface: "eth1"},
			EndpointB: model.LinkEndpoint{NodeID: "n-leaf", Interface: "eth1"},
		}},
		{Spec: model.LinkSpec{
			EndpointA: model.LinkEndpoint{NodeID: "n-leaf", Interface: "eth2"},
			EndpointB: model.LinkEndpoint{NodeID: "n-srv", Interface: "eth2"},
		}},
	}

	got := simulatedInterfaces([]*model.Node{leaf, server, spine}, links)

	want := map[string][]string{
		"leaf-b":   {"eth1", "eth2", "eth10", "vlan1000"},
		"server-1": {"eth1", "eth2", "bond0"},
		"spine-1":  {"eth1"},
	}

	for name, ifaces := range want {
		if !slices.Equal(got[name], ifaces) {
			t.Errorf("%s: got %v, want %v", name, got[name], ifaces)
		}
	}
}

func TestInterfaceStatuses(t *testing.T) {
	states := parseInterfaceStates(`lo               UNKNOWN        00:00:00:00:00:00
eth0@if20        UP             02:42:ac:14:14:09
eth1@if21        UP             aa:c1:ab:00:00:01
eth3@if22        DOWN           aa:c1:ab:00:00:02
vlan1000@br0     UP             aa:c1:ab:00:00:03
vrrp4-1-1@vlan1000 DOWN         00:00:5e:00:01:01
`)

	tests := []struct {
		name   string
		ifaces []string
		want   []model.InterfaceStatus
	}{
		{
			// eth2's veth is gone entirely (e.g. a container restart
			// dropped it) and is flagged Missing; eth3 is present in
			// the kernel but administratively/operationally down
			// (e.g. an interface-down fault) and is not — the two
			// must stay distinguishable so the UI can tell drift from
			// an intentional fault.
			name:   "leaf counts links and vlanif; missing veth vs present-but-down",
			ifaces: []string{"eth1", "eth2", "eth3", "vlan1000"},
			want: []model.InterfaceStatus{
				{Name: "eth1", Up: true},
				{Name: "eth2", Up: false, Missing: true},
				{Name: "eth3", Up: false},
				{Name: "vlan1000", Up: true},
			},
		},
		{
			name:   "no simulated interfaces",
			ifaces: nil,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := interfaceStatuses(states, tt.ifaces)
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseVRRPState(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "master leaf",
			out:  `[{"vrid":1,"interface":"vlan1000","v4":{"status":"Master"},"v6":{"status":"Initialize"}}]`,
			want: "Master",
		},
		{
			name: "backup leaf with vtysh warning prefix",
			out:  "% some warning\n[{\"vrid\":1,\"v4\":{\"status\":\"Backup\"}}]",
			want: "Backup",
		},
		{name: "no vrrp instances", out: "[]", want: ""},
		{name: "vrrpd not running", out: "% VRRP is not enabled", want: ""},
		{name: "empty output", out: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseVRRPState([]byte(tt.out)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
