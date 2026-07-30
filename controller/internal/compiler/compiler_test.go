package compiler

import (
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ifantsai/dcnetlab/controller/internal/compiler/containerlab"
	"github.com/ifantsai/dcnetlab/internal/model"
)

var update = flag.Bool("update", false, "update golden files")

// fixture: one rack — spine-1 over the MLAG pair leaf-a/leaf-b with a
// dual-homed server-1 on VLAN 1000 (10.100.0.0/24: .1 virtual
// gateway, .2/.3 leaf vlanifs, .11 server).
func fixture() (*model.Lab, []*model.Node, []*model.Link) {
	lab := &model.Lab{Meta: model.ResourceMeta{ID: "lab-1", Name: "golden"}}
	gw := netip.MustParseAddr("10.100.0.1")
	spine := &model.Node{
		Meta: model.ResourceMeta{ID: "n-spine", Name: "spine-1"},
		Spec: model.NodeSpec{
			LabID: "lab-1", Role: model.RoleSpine, ASN: 65200,
			Loopback:    netip.MustParsePrefix("10.1.0.0/32"),
			RuntimeType: model.RuntimeFRR,
		},
	}

	leafA := &model.Node{
		Meta: model.ResourceMeta{ID: "n-leaf-a", Name: "leaf-a"},
		Spec: model.NodeSpec{
			LabID: "lab-1", Role: model.RoleLeaf, RackID: "rack-1", ASN: 4200080001,
			Loopback:     netip.MustParsePrefix("10.1.0.1/32"),
			RuntimeType:  model.RuntimeFRR,
			MLAGPeer:     "leaf-b",
			VlanID:       1000,
			VlanIP:       netip.MustParsePrefix("10.100.0.2/24"),
			GatewayIP:    gw,
			GatewayMAC:   "00:00:5e:00:01:01",
			VRRPGroup:    1,
			VRRPPriority: 200,
		},
	}

	leafB := &model.Node{
		Meta: model.ResourceMeta{ID: "n-leaf-b", Name: "leaf-b"},
		Spec: model.NodeSpec{
			LabID: "lab-1", Role: model.RoleLeaf, RackID: "rack-1", ASN: 4200080001,
			Loopback:     netip.MustParsePrefix("10.1.0.2/32"),
			RuntimeType:  model.RuntimeFRR,
			MLAGPeer:     "leaf-a",
			VlanID:       1000,
			VlanIP:       netip.MustParsePrefix("10.100.0.3/24"),
			GatewayIP:    gw,
			GatewayMAC:   "00:00:5e:00:01:01",
			VRRPGroup:    1,
			VRRPPriority: 100,
		},
	}

	server := &model.Node{
		Meta: model.ResourceMeta{ID: "n-srv", Name: "server-1"},
		Spec: model.NodeSpec{
			LabID: "lab-1", Role: model.RoleServer, RackID: "rack-1", ASN: 65000,
			RuntimeType:    model.RuntimeFRR,
			Address:        netip.MustParsePrefix("10.100.0.11/24"),
			DefaultGateway: gw,
			BGPPeers:       []netip.Addr{netip.MustParseAddr("10.100.0.2"), netip.MustParseAddr("10.100.0.3")},
		},
	}

	p2p := func(id, aID, aName, aIf, aAddr, zID, zName, zIf, zAddr string) *model.Link {
		return &model.Link{
			Meta: model.ResourceMeta{ID: id, Name: aName + "--" + zName},
			Spec: model.LinkSpec{
				LabID: "lab-1", Kind: model.LinkFabric,
				EndpointA: model.LinkEndpoint{NodeID: aID, NodeName: aName, Interface: aIf, Address: netip.MustParsePrefix(aAddr)},
				EndpointB: model.LinkEndpoint{NodeID: zID, NodeName: zName, Interface: zIf, Address: netip.MustParsePrefix(zAddr)},
				MTU:       9100,
			},
		}
	}

	l2 := func(id string, kind model.LinkKind, aID, aName, aIf, zID, zName, zIf string) *model.Link {
		return &model.Link{
			Meta: model.ResourceMeta{ID: id, Name: aName + "--" + zName},
			Spec: model.LinkSpec{
				LabID: "lab-1", Kind: kind, VlanID: 1000,
				EndpointA: model.LinkEndpoint{NodeID: aID, NodeName: aName, Interface: aIf},
				EndpointB: model.LinkEndpoint{NodeID: zID, NodeName: zName, Interface: zIf},
				MTU:       9100,
			},
		}
	}

	links := []*model.Link{
		p2p("l-1", "n-spine", "spine-1", "eth1", "10.0.0.0/31", "n-leaf-a", "leaf-a", "eth1", "10.0.0.1/31"),
		p2p("l-2", "n-spine", "spine-1", "eth2", "10.0.0.2/31", "n-leaf-b", "leaf-b", "eth1", "10.0.0.3/31"),
		l2("l-3", model.LinkMLAGPeer, "n-leaf-a", "leaf-a", "eth2", "n-leaf-b", "leaf-b", "eth2"),
		l2("l-4", model.LinkServerAccess, "n-leaf-a", "leaf-a", "eth3", "n-srv", "server-1", "eth1"),
		l2("l-5", model.LinkServerAccess, "n-leaf-b", "leaf-b", "eth3", "n-srv", "server-1", "eth2"),
	}

	return lab, []*model.Node{spine, leafA, leafB, server}, links
}

func TestCompileGolden(t *testing.T) {
	lab, nodes, links := fixture()

	opts := containerlab.DefaultOptions()

	art, err := Compile(lab, nodes, links, opts)
	if err != nil {
		t.Fatal(err)
	}

	golden := map[string][]byte{
		"topology.clab.yml":         art.ClabTopology,
		"configs/spine-1/frr.conf":  art.Files["configs/spine-1/frr.conf"],
		"configs/leaf-a/frr.conf":   art.Files["configs/leaf-a/frr.conf"],
		"configs/leaf-b/frr.conf":   art.Files["configs/leaf-b/frr.conf"],
		"configs/server-1/frr.conf": art.Files["configs/server-1/frr.conf"],
		"configs/spine-1/daemons":   art.Files["configs/spine-1/daemons"],
	}

	for name, got := range golden {
		if got == nil {
			t.Fatalf("artifact %s missing", name)
		}

		path := filepath.Join("testdata", "golden", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}

			continue
		}

		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden %s: %v (run go test -update)", name, err)
		}

		if string(got) != string(want) {
			t.Errorf("%s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}
}

// internetFixture: external-1 <-> dcedge-1, the tiers involved in the
// internet exit path.
func internetFixture(internetAccess bool) (*model.Lab, []*model.Node, []*model.Link) {
	lab := &model.Lab{
		Meta: model.ResourceMeta{ID: "lab-1", Name: "golden"},
		Spec: model.LabSpec{Topology: model.TopologySpec{InternetAccess: internetAccess}},
	}

	external := &model.Node{
		Meta: model.ResourceMeta{ID: "n-ext", Name: "external-1"},
		Spec: model.NodeSpec{
			LabID: "lab-1", Role: model.RoleExternal, ASN: 64500,
			Loopback:    netip.MustParsePrefix("10.1.0.0/32"),
			RuntimeType: model.RuntimeFRR,
		},
	}

	dcedge := &model.Node{
		Meta: model.ResourceMeta{ID: "n-edge", Name: "dcedge-1"},
		Spec: model.NodeSpec{
			LabID: "lab-1", Role: model.RoleDCEdge, ASN: 64600,
			Loopback:    netip.MustParsePrefix("10.1.0.1/32"),
			RuntimeType: model.RuntimeFRR,
		},
	}

	links := []*model.Link{{
		Meta: model.ResourceMeta{ID: "l-1", Name: "external-1--dcedge-1"},
		Spec: model.LinkSpec{
			LabID: "lab-1", Kind: model.LinkFabric,
			EndpointA: model.LinkEndpoint{NodeID: "n-ext", NodeName: "external-1", Interface: "eth1", Address: netip.MustParsePrefix("10.0.0.0/31")},
			EndpointB: model.LinkEndpoint{NodeID: "n-edge", NodeName: "dcedge-1", Interface: "eth1", Address: netip.MustParsePrefix("10.0.0.1/31")},
			MTU:       9100,
		},
	}}

	return lab, []*model.Node{external, dcedge}, links
}

// TestInternetAccessArtifacts guards the internet exit path: the
// external originates a default route towards its dcedge, the dcedge
// SNATs internet-bound traffic to its loopback, and both run the
// iptables-capable edge image. With the toggle off none of this may
// appear.
func TestInternetAccessArtifacts(t *testing.T) {
	lab, nodes, links := internetFixture(true)
	art, err := Compile(lab, nodes, links, containerlab.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}

	extConf := string(art.Files["configs/external-1/frr.conf"])
	if want := "neighbor 10.0.0.1 default-originate"; !strings.Contains(extConf, want) {
		t.Errorf("external config missing %q:\n%s", want, extConf)
	}

	if edgeConf := string(art.Files["configs/dcedge-1/frr.conf"]); strings.Contains(edgeConf, "default-originate") {
		t.Errorf("dcedge must not originate a default route:\n%s", edgeConf)
	}

	topo := string(art.ClabTopology)
	for _, want := range []string{
		"iptables -t nat -A POSTROUTING ! -d 10.0.0.0/8 ! -o eth0 -j SNAT --to-source 10.1.0.1",
		"dcnetlab/frr-edge:10.2.1",
	} {
		if !strings.Contains(topo, want) {
			t.Errorf("clab topology missing %q:\n%s", want, topo)
		}
	}

	lab, nodes, links = internetFixture(false)
	art, err = Compile(lab, nodes, links, containerlab.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}

	if conf := string(art.Files["configs/external-1/frr.conf"]); strings.Contains(conf, "default-originate") {
		t.Errorf("air-gapped external must not originate a default route:\n%s", conf)
	}

	if topo := string(art.ClabTopology); strings.Contains(topo, "iptables") || strings.Contains(topo, "frr-edge") {
		t.Errorf("air-gapped topology must not carry NAT or the edge image:\n%s", topo)
	}
}

// TestServerBGPUsesPhysicalLeafIPs guards the core VRRP constraint:
// servers must peer with the leaves' physical vlanif addresses and
// never with the shared virtual gateway address.
func TestServerBGPUsesPhysicalLeafIPs(t *testing.T) {
	lab, nodes, links := fixture()
	art, err := Compile(lab, nodes, links, containerlab.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}

	conf := string(art.Files["configs/server-1/frr.conf"])
	if conf == "" {
		t.Fatal("server has no FRR config")
	}

	for _, want := range []string{
		"router bgp 65000",
		"neighbor 10.100.0.2 remote-as 4200080001",
		"neighbor 10.100.0.3 remote-as 4200080001",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("server config missing %q:\n%s", want, conf)
		}
	}

	if strings.Contains(conf, "neighbor 10.100.0.1 ") {
		t.Errorf("server must not peer with the virtual gateway:\n%s", conf)
	}
}

// TestLeafAdvertisesOnlyDefaultToServers guards the access-layer
// aggregation: servers receive one originated default route instead
// of the full fabric table.
func TestLeafAdvertisesOnlyDefaultToServers(t *testing.T) {
	lab, nodes, links := fixture()
	art, err := Compile(lab, nodes, links, containerlab.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}

	for _, leaf := range []string{"leaf-a", "leaf-b"} {
		conf := string(art.Files["configs/"+leaf+"/frr.conf"])
		for _, want := range []string{
			"neighbor SERVERS default-originate",
			"neighbor SERVERS prefix-list SERVERS-OUT out",
			"ip prefix-list SERVERS-OUT seq 10 permit 0.0.0.0/0",
		} {
			if !strings.Contains(conf, want) {
				t.Errorf("%s config missing %q:\n%s", leaf, want, conf)
			}
		}
	}

	if conf := string(art.Files["configs/spine-1/frr.conf"]); strings.Contains(conf, "SERVERS-OUT") {
		t.Errorf("spine must not carry the server export filter:\n%s", conf)
	}
}
