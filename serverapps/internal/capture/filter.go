package capture

import (
	"fmt"
	"net/netip"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

// Protocol filter values. BGP and VXLAN are conveniences over their
// well-known ports; everything else matches on decoded layers.
const (
	ProtoARP   = "arp"
	ProtoICMP  = "icmp"
	ProtoTCP   = "tcp"
	ProtoUDP   = "udp"
	ProtoBGP   = "bgp"
	ProtoVXLAN = "vxlan"
)

const (
	bgpPort   = 179
	vxlanPort = 4789
)

// Filter is the structured capture filter, matched in user space
// against each decoded frame. The zero value matches everything.
type Filter struct {
	// Protocol restricts frames to one protocol; empty means any.
	Protocol string
	// Src and Dst restrict the network-layer addresses; an invalid
	// (zero) prefix means any. Frames without a network layer (e.g.
	// ARP) never match an address restriction.
	Src netip.Prefix
	Dst netip.Prefix
	// Port matches either transport port; 0 means any. Frames without
	// a transport layer never match a port restriction.
	Port uint16
}

func (f Filter) validate() error {
	switch f.Protocol {
	case "", ProtoARP, ProtoICMP, ProtoTCP, ProtoUDP, ProtoBGP, ProtoVXLAN:
	default:
		return fmt.Errorf("unknown protocol filter %q", f.Protocol)
	}

	return nil
}

// Match reports whether the frame passes the filter. Decoding is lazy:
// frames are only parsed as deep as the configured restrictions need.
func (f Filter) Match(frame []byte) bool {
	if f.Protocol == "" && !f.Src.IsValid() && !f.Dst.IsValid() && f.Port == 0 {
		return true
	}

	pkt := gopacket.NewPacket(frame, layers.LayerTypeEthernet, gopacket.DecodeOptions{Lazy: true, NoCopy: true})

	if !f.matchProtocol(pkt) {
		return false
	}

	if f.Src.IsValid() || f.Dst.IsValid() {
		src, dst, ok := networkAddrs(pkt)
		if !ok {
			return false
		}

		if f.Src.IsValid() && !f.Src.Contains(src) {
			return false
		}

		if f.Dst.IsValid() && !f.Dst.Contains(dst) {
			return false
		}
	}

	if f.Port != 0 {
		srcPort, dstPort, ok := transportPorts(pkt)
		if !ok || (srcPort != f.Port && dstPort != f.Port) {
			return false
		}
	}

	return true
}

func (f Filter) matchProtocol(pkt gopacket.Packet) bool {
	switch f.Protocol {
	case "":
		return true
	case ProtoARP:
		return pkt.Layer(layers.LayerTypeARP) != nil
	case ProtoICMP:
		return pkt.Layer(layers.LayerTypeICMPv4) != nil || pkt.Layer(layers.LayerTypeICMPv6) != nil
	case ProtoTCP:
		return pkt.Layer(layers.LayerTypeTCP) != nil
	case ProtoUDP:
		return pkt.Layer(layers.LayerTypeUDP) != nil
	case ProtoBGP:
		return hasPort(pkt, layers.LayerTypeTCP, bgpPort)
	case ProtoVXLAN:
		return hasPort(pkt, layers.LayerTypeUDP, vxlanPort)
	default:
		return false
	}
}

func hasPort(pkt gopacket.Packet, transport gopacket.LayerType, port uint16) bool {
	if pkt.Layer(transport) == nil {
		return false
	}

	src, dst, ok := transportPorts(pkt)

	return ok && (src == port || dst == port)
}

func networkAddrs(pkt gopacket.Packet) (src, dst netip.Addr, ok bool) {
	switch l := pkt.NetworkLayer().(type) {
	case *layers.IPv4:
		src, ok = netip.AddrFromSlice(l.SrcIP)
		if !ok {
			return netip.Addr{}, netip.Addr{}, false
		}

		dst, ok = netip.AddrFromSlice(l.DstIP)

		return src.Unmap(), dst.Unmap(), ok
	case *layers.IPv6:
		src, ok = netip.AddrFromSlice(l.SrcIP)
		if !ok {
			return netip.Addr{}, netip.Addr{}, false
		}

		dst, ok = netip.AddrFromSlice(l.DstIP)

		return src, dst, ok
	default:
		return netip.Addr{}, netip.Addr{}, false
	}
}

func transportPorts(pkt gopacket.Packet) (src, dst uint16, ok bool) {
	switch l := pkt.TransportLayer().(type) {
	case *layers.TCP:
		return uint16(l.SrcPort), uint16(l.DstPort), true
	case *layers.UDP:
		return uint16(l.SrcPort), uint16(l.DstPort), true
	default:
		return 0, 0, false
	}
}
