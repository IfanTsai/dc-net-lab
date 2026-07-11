// Package frr compiles FRR configuration files from the desired
// topology. Configs are pure artifacts: they never introduce state
// that is not present in the resource model.
package frr

import (
	"bytes"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// InterfaceConfig is one addressed interface of a router.
type InterfaceConfig struct {
	Name     string
	Address  netip.Prefix
	PeerName string
}

// NeighborConfig is one eBGP neighbor.
type NeighborConfig struct {
	Address  netip.Addr
	RemoteAS uint32
	Name     string
}

// VlanConfig is the leaf's server-gateway VLAN interface: its own
// physical address plus the VRRP virtual gateway shared with the
// MLAG peer leaf.
type VlanConfig struct {
	ID        int
	RackID    string
	Address   netip.Prefix // physical vlanif address, unique per leaf
	GatewayIP netip.Addr   // VRRP virtual IP, shared by the pair
	VRID      int
	Priority  int
}

// ServerGroupConfig is the leaf's BGP peer-group towards the rack's
// servers, listening on the whole rack VLAN subnet. Servers only get
// an originated default route (fabric detail stays inside the
// fabric), like a real access layer.
type ServerGroupConfig struct {
	ASN         uint32
	ListenRange netip.Prefix
}

// RouterConfig is everything needed to render one node's frr.conf.
// Servers use it too: no loopback, bond0 as the only interface and
// the leaf physical vlanif addresses as neighbors.
type RouterConfig struct {
	Hostname    string
	ASN         uint32
	RouterID    netip.Addr
	Loopback    netip.Prefix
	Interfaces  []InterfaceConfig
	Vlan        *VlanConfig
	Neighbors   []NeighborConfig
	ServerGroup *ServerGroupConfig
}

const frrTemplate = `frr defaults datacenter
hostname {{ .Hostname }}
service integrated-vtysh-config
!
{{- if .Loopback.IsValid }}
interface lo
 ip address {{ .Loopback }}
!
{{- end }}
{{- range .Interfaces }}
interface {{ .Name }}
 description to {{ .PeerName }}
 ip address {{ .Address }}
!
{{- end }}
{{- with .Vlan }}
interface vlan{{ .ID }}
 description server gateway ({{ .RackID }})
 ip address {{ .Address }}
 vrrp {{ .VRID }} priority {{ .Priority }}
 vrrp {{ .VRID }} ip {{ .GatewayIP }}
!
{{- end }}
router bgp {{ .ASN }}
 bgp router-id {{ .RouterID }}
 bgp bestpath as-path multipath-relax
 no bgp ebgp-requires-policy
{{- range .Neighbors }}
 neighbor {{ .Address }} remote-as {{ .RemoteAS }}
 neighbor {{ .Address }} description {{ .Name }}
{{- end }}
{{- with .ServerGroup }}
 neighbor SERVERS peer-group
 neighbor SERVERS remote-as {{ .ASN }}
 bgp listen range {{ .ListenRange }} peer-group SERVERS
{{- end }}
 !
 address-family ipv4 unicast
  redistribute connected
  maximum-paths 64
{{- if .ServerGroup }}
  neighbor SERVERS default-originate
  neighbor SERVERS prefix-list SERVERS-OUT out
{{- end }}
 exit-address-family
!
{{- if .ServerGroup }}
ip prefix-list SERVERS-OUT seq 10 permit 0.0.0.0/0
!
{{- end }}
line vty
!
`

// Daemons is the FRR daemons file: bgpd everywhere, vrrpd for the
// leaf gateway pairs (idle elsewhere), bfdd for later use.
const Daemons = `bgpd=yes
bfdd=yes
vrrpd=yes
zebra=yes
vtysh_enable=yes
`

// VtyshConf is bound to /etc/frr/vtysh.conf so vtysh does not print
// warnings before its output (which would corrupt JSON parsing).
const VtyshConf = `service integrated-vtysh-config
`

var tmpl = template.Must(template.New("frr").Parse(frrTemplate))

// Render renders one router's frr.conf.
func Render(cfg RouterConfig) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, cfg); err != nil {
		return nil, fmt.Errorf("render frr config for %s: %w", cfg.Hostname, err)
	}

	return buf.Bytes(), nil
}

// BuildRouterConfigs derives per-node FRR configs from nodes and
// links. Routers speak eBGP over addressed fabric links; each MLAG
// leaf additionally gets its gateway VLAN (with VRRP), a peer-group
// listening for the rack's servers and an iBGP session to its MLAG
// peer. Servers get a bond0 config peering with the physical vlanif
// addresses of both leaves.
func BuildRouterConfigs(nodes []*model.Node, links []*model.Link) (map[string]RouterConfig, error) {
	byID := make(map[string]*model.Node, len(nodes))
	byName := make(map[string]*model.Node, len(nodes))
	byVlanIP := make(map[netip.Addr]*model.Node)
	serverASNByRack := make(map[string]uint32)
	for _, n := range nodes {
		byID[n.Meta.ID] = n
		byName[n.Meta.Name] = n
		if n.Spec.VlanIP.IsValid() {
			byVlanIP[n.Spec.VlanIP.Addr()] = n
		}

		if n.Spec.Role == model.RoleServer && n.Spec.RackID != "" {
			serverASNByRack[n.Spec.RackID] = n.Spec.ASN
		}
	}

	cfgs := make(map[string]RouterConfig)
	for _, n := range nodes {
		if !n.Spec.IsRouter() {
			continue
		}

		if n.Spec.Role == model.RoleServer {
			cfgs[n.Meta.ID] = serverConfig(n, byVlanIP)
			continue
		}

		cfg := RouterConfig{
			Hostname: n.Meta.Name,
			ASN:      n.Spec.ASN,
			RouterID: n.Spec.Loopback.Addr(),
			Loopback: n.Spec.Loopback,
		}

		if n.Spec.VlanID != 0 {
			cfg.Vlan = &VlanConfig{
				ID:        n.Spec.VlanID,
				RackID:    n.Spec.RackID,
				Address:   n.Spec.VlanIP,
				GatewayIP: n.Spec.GatewayIP,
				VRID:      n.Spec.VRRPGroup,
				Priority:  n.Spec.VRRPPriority,
			}

			if srvASN, ok := serverASNByRack[n.Spec.RackID]; ok {
				cfg.ServerGroup = &ServerGroupConfig{
					ASN:         srvASN,
					ListenRange: n.Spec.VlanIP.Masked(),
				}
			}

			// iBGP to the MLAG peer over the shared VLAN, using its
			// physical address.
			if peer, ok := byName[n.Spec.MLAGPeer]; ok {
				cfg.Neighbors = append(cfg.Neighbors, NeighborConfig{
					Address:  peer.Spec.VlanIP.Addr(),
					RemoteAS: peer.Spec.ASN,
					Name:     peer.Meta.Name + " (MLAG peer)",
				})
			}
		}

		cfgs[n.Meta.ID] = cfg
	}

	for _, l := range links {
		a, okA := byID[l.Spec.EndpointA.NodeID]
		z, okZ := byID[l.Spec.EndpointB.NodeID]
		if !okA || !okZ {
			return nil, fmt.Errorf("link %s references unknown node", l.Meta.Name)
		}

		addSide(cfgs, a, z, l.Spec.EndpointA, l.Spec.EndpointB)
		addSide(cfgs, z, a, l.Spec.EndpointB, l.Spec.EndpointA)
	}

	for id, cfg := range cfgs {
		sortInterfaces(cfg.Interfaces)
		sort.Slice(cfg.Neighbors, func(i, j int) bool {
			return cfg.Neighbors[i].Address.Less(cfg.Neighbors[j].Address)
		})
		cfgs[id] = cfg
	}

	return cfgs, nil
}

// serverConfig builds the FRR config of a dual-homed server: bond0 on
// the rack VLAN, eBGP to the physical vlanif address of each leaf.
func serverConfig(n *model.Node, byVlanIP map[netip.Addr]*model.Node) RouterConfig {
	cfg := RouterConfig{
		Hostname: n.Meta.Name,
		ASN:      n.Spec.ASN,
		RouterID: n.Spec.Address.Addr(),
		Interfaces: []InterfaceConfig{
			{Name: "bond0", Address: n.Spec.Address, PeerName: n.Spec.RackID + " leaf pair"},
		},
	}

	for _, peerIP := range n.Spec.BGPPeers {
		nb := NeighborConfig{Address: peerIP, Name: peerIP.String()}
		if leaf, ok := byVlanIP[peerIP]; ok {
			nb.RemoteAS = leaf.Spec.ASN
			nb.Name = leaf.Meta.Name
		}

		cfg.Neighbors = append(cfg.Neighbors, nb)
	}

	return cfg
}

// addSide records local's interface on an addressed fabric link and
// an eBGP neighbor towards the peer router. L2 links (server access,
// MLAG peer) carry no addresses and are realised outside FRR.
func addSide(cfgs map[string]RouterConfig, local, peer *model.Node, lep, pep model.LinkEndpoint) {
	if !lep.Address.IsValid() {
		return
	}

	cfg, ok := cfgs[local.Meta.ID]
	if !ok {
		return
	}

	cfg.Interfaces = append(cfg.Interfaces, InterfaceConfig{
		Name:     lep.Interface,
		Address:  lep.Address,
		PeerName: peer.Meta.Name,
	})
	if peer.Spec.IsRouter() && pep.Address.IsValid() {
		cfg.Neighbors = append(cfg.Neighbors, NeighborConfig{
			Address:  pep.Address.Addr(),
			RemoteAS: peer.Spec.ASN,
			Name:     peer.Meta.Name,
		})
	}

	cfgs[local.Meta.ID] = cfg
}

// sortInterfaces orders eth1, eth2, ..., eth10 numerically.
func sortInterfaces(ifs []InterfaceConfig) {
	sort.Slice(ifs, func(i, j int) bool {
		return ifNum(ifs[i].Name) < ifNum(ifs[j].Name)
	})
}

func ifNum(name string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(name, "eth"))

	return n
}
