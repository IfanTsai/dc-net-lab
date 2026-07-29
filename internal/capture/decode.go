// Package capture is the controller side of packet capture: it runs
// capture sessions over the runtime driver's exec stream, keeps a
// live in-memory window of decoded packets per session, writes the
// pcapng recording to disk and decodes packets for the viewer. The
// in-container tool lives in serverapps/capture; this package never
// touches AF_PACKET itself.
package capture

import (
	"fmt"
	"strings"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

// bgpPort identifies BGP conversations; gopacket has no BGP decoder,
// so bgp.go provides a minimal one for the platform's core protocol.
const bgpPort = 179

// Summary is the packet list row: Wireshark's source, destination,
// protocol and info columns.
type Summary struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Protocol    string `json:"protocol"`
	Info        string `json:"info"`
}

// Field is one name/value line of a decoded layer.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Layer is one protocol layer of the packet detail tree.
type Layer struct {
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
}

// parse decodes a frame fully; decoding errors surface as an error
// layer, not a failure.
func parse(data []byte) gopacket.Packet {
	return gopacket.NewPacket(data, layers.LayerTypeEthernet, gopacket.DecodeOptions{NoCopy: true})
}

// Summarize builds the list row of one frame. Addresses come from the
// innermost network layer (so a VXLAN-encapsulated frame shows its
// inner conversation), protocol from the innermost recognised layer.
func Summarize(data []byte) Summary {
	pkt := parse(data)

	var (
		eth   *layers.Ethernet
		arp   *layers.ARP
		ip4   *layers.IPv4
		ip6   *layers.IPv6
		icmp4 *layers.ICMPv4
		icmp6 *layers.ICMPv6
		tcp   *layers.TCP
		udp   *layers.UDP
		vxlan *layers.VXLAN
	)
	for _, l := range pkt.Layers() {
		switch v := l.(type) {
		case *layers.Ethernet:
			if eth == nil {
				eth = v
			}

		case *layers.ARP:
			arp = v
		case *layers.IPv4:
			ip4 = v

			ip6 = nil
		case *layers.IPv6:
			ip6 = v

			ip4 = nil
		case *layers.ICMPv4:
			icmp4 = v
		case *layers.ICMPv6:
			icmp6 = v
		case *layers.TCP:
			tcp = v

			udp = nil
		case *layers.UDP:
			udp = v

			tcp = nil
		case *layers.VXLAN:
			vxlan = v
		}
	}

	s := Summary{Protocol: "Ethernet"}

	switch {
	case ip4 != nil:
		s.Source, s.Destination = ip4.SrcIP.String(), ip4.DstIP.String()
	case ip6 != nil:
		s.Source, s.Destination = ip6.SrcIP.String(), ip6.DstIP.String()
	case arp != nil:
		s.Source = fmt.Sprintf("%d.%d.%d.%d", arp.SourceProtAddress[0], arp.SourceProtAddress[1], arp.SourceProtAddress[2], arp.SourceProtAddress[3])
		s.Destination = fmt.Sprintf("%d.%d.%d.%d", arp.DstProtAddress[0], arp.DstProtAddress[1], arp.DstProtAddress[2], arp.DstProtAddress[3])
	case eth != nil:
		s.Source, s.Destination = eth.SrcMAC.String(), eth.DstMAC.String()
	}

	switch {
	case tcp != nil && (tcp.SrcPort == bgpPort || tcp.DstPort == bgpPort) && len(tcp.Payload) > 0:
		if msgs := decodeBGP(tcp.Payload); len(msgs) > 0 {
			s.Protocol = "BGP"
			s.Info = bgpInfo(msgs)

			break
		}

		s.Protocol, s.Info = "TCP", tcpInfo(tcp)
	case icmp4 != nil:
		s.Protocol, s.Info = "ICMP", icmpInfo(icmp4.TypeCode.String(), icmp4.Id, icmp4.Seq)
	case icmp6 != nil:
		s.Protocol, s.Info = "ICMPv6", icmp6.TypeCode.String()
	case tcp != nil:
		s.Protocol, s.Info = "TCP", tcpInfo(tcp)
	case udp != nil:
		s.Protocol, s.Info = "UDP", fmt.Sprintf("%d → %d Len=%d", udp.SrcPort, udp.DstPort, len(udp.Payload))
	case arp != nil:
		s.Protocol, s.Info = "ARP", arpInfo(arp)
	case ip4 != nil:
		s.Protocol, s.Info = ip4.Protocol.String(), ""
	case ip6 != nil:
		s.Protocol, s.Info = ip6.NextHeader.String(), ""
	}

	if vxlan != nil {
		s.Info = fmt.Sprintf("VXLAN VNI=%d · %s", vxlan.VNI, s.Info)
	}

	if pkt.ErrorLayer() != nil && s.Info == "" {
		s.Info = "[undecodable]"
	}

	return s
}

func tcpInfo(tcp *layers.TCP) string {
	return fmt.Sprintf("%d → %d [%s] Seq=%d Ack=%d Win=%d Len=%d",
		tcp.SrcPort, tcp.DstPort, tcpFlags(tcp), tcp.Seq, tcp.Ack, tcp.Window, len(tcp.Payload))
}

func tcpFlags(tcp *layers.TCP) string {
	var flags []string
	for _, f := range []struct {
		set  bool
		name string
	}{{tcp.SYN, "SYN"}, {tcp.FIN, "FIN"}, {tcp.RST, "RST"}, {tcp.PSH, "PSH"}, {tcp.ACK, "ACK"}, {tcp.URG, "URG"}} {
		if f.set {
			flags = append(flags, f.name)
		}
	}

	return strings.Join(flags, ", ")
}

func icmpInfo(typeCode string, id, seq uint16) string {
	if id == 0 && seq == 0 {
		return typeCode
	}

	return fmt.Sprintf("%s id=%d seq=%d", typeCode, id, seq)
}

func arpInfo(arp *layers.ARP) string {
	sender := fmt.Sprintf("%d.%d.%d.%d", arp.SourceProtAddress[0], arp.SourceProtAddress[1], arp.SourceProtAddress[2], arp.SourceProtAddress[3])
	target := fmt.Sprintf("%d.%d.%d.%d", arp.DstProtAddress[0], arp.DstProtAddress[1], arp.DstProtAddress[2], arp.DstProtAddress[3])
	if arp.Operation == layers.ARPRequest {
		return fmt.Sprintf("Who has %s? Tell %s", target, sender)
	}

	hw := fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		arp.SourceHwAddress[0], arp.SourceHwAddress[1], arp.SourceHwAddress[2],
		arp.SourceHwAddress[3], arp.SourceHwAddress[4], arp.SourceHwAddress[5])

	return fmt.Sprintf("%s is at %s", sender, hw)
}

// Tree decodes a frame into the viewer's protocol layer tree. The TCP
// payload of a BGP conversation becomes one tree layer per BGP
// message; any other undecoded payload shows as a data layer.
func Tree(data []byte) []Layer {
	pkt := parse(data)

	var out []Layer
	bgpDone := false
	for _, l := range pkt.Layers() {
		switch v := l.(type) {
		case *layers.Ethernet:
			out = append(out, Layer{Name: "Ethernet", Fields: []Field{
				{"Source", v.SrcMAC.String()},
				{"Destination", v.DstMAC.String()},
				{"Type", v.EthernetType.String()},
			}})

		case *layers.ARP:
			out = append(out, arpLayer(v))
		case *layers.Dot1Q:
			out = append(out, Layer{Name: "802.1Q VLAN", Fields: []Field{
				{"VLAN ID", fmt.Sprintf("%d", v.VLANIdentifier)},
				{"Priority", fmt.Sprintf("%d", v.Priority)},
				{"Type", v.Type.String()},
			}})

		case *layers.IPv4:
			out = append(out, ipv4Layer(v))
		case *layers.IPv6:
			out = append(out, Layer{Name: "IPv6", Fields: []Field{
				{"Source", v.SrcIP.String()},
				{"Destination", v.DstIP.String()},
				{"Next Header", v.NextHeader.String()},
				{"Hop Limit", fmt.Sprintf("%d", v.HopLimit)},
				{"Payload Length", fmt.Sprintf("%d", v.Length)},
			}})

		case *layers.ICMPv4:
			out = append(out, Layer{Name: "ICMP", Fields: []Field{
				{"Type/Code", v.TypeCode.String()},
				{"Checksum", fmt.Sprintf("0x%04x", v.Checksum)},
				{"Identifier", fmt.Sprintf("%d", v.Id)},
				{"Sequence", fmt.Sprintf("%d", v.Seq)},
			}})

		case *layers.ICMPv6:
			out = append(out, Layer{Name: "ICMPv6", Fields: []Field{
				{"Type/Code", v.TypeCode.String()},
				{"Checksum", fmt.Sprintf("0x%04x", v.Checksum)},
			}})

		case *layers.TCP:
			out = append(out, tcpLayer(v))
			if (v.SrcPort == bgpPort || v.DstPort == bgpPort) && len(v.Payload) > 0 {
				if msgs := decodeBGP(v.Payload); len(msgs) > 0 {
					for _, m := range msgs {
						out = append(out, Layer{Name: "BGP · " + m.Type, Fields: m.Fields})
					}

					bgpDone = true
				}
			}

		case *layers.UDP:
			out = append(out, Layer{Name: "UDP", Fields: []Field{
				{"Source Port", fmt.Sprintf("%d", v.SrcPort)},
				{"Destination Port", fmt.Sprintf("%d", v.DstPort)},
				{"Length", fmt.Sprintf("%d", v.Length)},
				{"Checksum", fmt.Sprintf("0x%04x", v.Checksum)},
			}})

		case *layers.VXLAN:
			out = append(out, Layer{Name: "VXLAN", Fields: []Field{
				{"VNI", fmt.Sprintf("%d", v.VNI)},
				{"Flags", fmt.Sprintf("0x%02x", flagByte(v.ValidIDFlag))},
			}})

		case *gopacket.Payload:
			if bgpDone {
				continue
			}

			out = append(out, Layer{Name: "Data", Fields: []Field{
				{"Length", fmt.Sprintf("%d bytes", len(v.Payload()))},
			}})

		case gopacket.ErrorLayer:
			out = append(out, Layer{Name: "Undecoded", Fields: []Field{
				{"Length", fmt.Sprintf("%d bytes", len(v.LayerContents()))},
			}})

		default:
			out = append(out, Layer{Name: l.LayerType().String(), Fields: []Field{
				{"Length", fmt.Sprintf("%d bytes", len(l.LayerContents()))},
			}})
		}
	}

	return out
}

func arpLayer(v *layers.ARP) Layer {
	op := "request"
	if v.Operation == layers.ARPReply {
		op = "reply"
	}

	return Layer{Name: "ARP", Fields: []Field{
		{"Operation", op},
		{"Sender MAC", fmt.Sprintf("% x", v.SourceHwAddress)},
		{"Sender IP", fmt.Sprintf("%d.%d.%d.%d", v.SourceProtAddress[0], v.SourceProtAddress[1], v.SourceProtAddress[2], v.SourceProtAddress[3])},
		{"Target MAC", fmt.Sprintf("% x", v.DstHwAddress)},
		{"Target IP", fmt.Sprintf("%d.%d.%d.%d", v.DstProtAddress[0], v.DstProtAddress[1], v.DstProtAddress[2], v.DstProtAddress[3])},
	}}
}

func ipv4Layer(v *layers.IPv4) Layer {
	return Layer{Name: "IPv4", Fields: []Field{
		{"Source", v.SrcIP.String()},
		{"Destination", v.DstIP.String()},
		{"Protocol", v.Protocol.String()},
		{"TTL", fmt.Sprintf("%d", v.TTL)},
		{"Identification", fmt.Sprintf("0x%04x", v.Id)},
		{"Flags", v.Flags.String()},
		{"Total Length", fmt.Sprintf("%d", v.Length)},
		{"Checksum", fmt.Sprintf("0x%04x", v.Checksum)},
	}}
}

func tcpLayer(v *layers.TCP) Layer {
	return Layer{Name: "TCP", Fields: []Field{
		{"Source Port", fmt.Sprintf("%d", v.SrcPort)},
		{"Destination Port", fmt.Sprintf("%d", v.DstPort)},
		{"Sequence", fmt.Sprintf("%d", v.Seq)},
		{"Acknowledgment", fmt.Sprintf("%d", v.Ack)},
		{"Flags", tcpFlags(v)},
		{"Window", fmt.Sprintf("%d", v.Window)},
		{"Checksum", fmt.Sprintf("0x%04x", v.Checksum)},
		{"Payload Length", fmt.Sprintf("%d", len(v.Payload))},
	}}
}

func flagByte(valid bool) int {
	if valid {
		return 0x08
	}

	return 0
}
