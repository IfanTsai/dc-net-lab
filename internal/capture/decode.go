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
// protocol and info columns. SourcePort/DestinationPort are set only
// for TCP/UDP conversations.
type Summary struct {
	Source          string `json:"source"`
	Destination     string `json:"destination"`
	SourcePort      int32  `json:"sourcePort,omitempty"`
	DestinationPort int32  `json:"destinationPort,omitempty"`
	Protocol        string `json:"protocol"`
	Info            string `json:"info"`
}

// Field is one name/value line of a decoded layer. Offset/Length locate
// the field's bytes in the raw frame for the viewer's hex highlight
// sync; Length 0 means the field has no fixed byte range (e.g. a
// derived value such as a payload length). Children nest sub-fields
// (e.g. each path attribute under the BGP UPDATE "Path Attributes"
// line).
type Field struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Offset   int     `json:"offset"`
	Length   int     `json:"length"`
	Children []Field `json:"children,omitempty"`
}

// newField builds a Field with no associated byte range.
func newField(name, value string) Field {
	return Field{Name: name, Value: value}
}

// newFieldAt builds a Field highlighting [offset, offset+length) of the
// raw frame.
func newFieldAt(name, value string, offset, length int) Field {
	return Field{Name: name, Value: value, Offset: offset, Length: length}
}

// shiftFields moves a field tree's byte ranges by delta; fields with
// no range (Length 0) stay untouched. The BGP decoder emits offsets
// relative to the TCP payload, and Tree shifts them frame-absolute.
func shiftFields(fields []Field, delta int) {
	for i := range fields {
		if fields[i].Length > 0 {
			fields[i].Offset += delta
		}

		shiftFields(fields[i].Children, delta)
	}
}

// Layer is one protocol layer of the packet detail tree. Offset/Length
// is the layer's own byte range in the raw frame.
type Layer struct {
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
	Offset int     `json:"offset"`
	Length int     `json:"length"`
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
	case tcp != nil:
		s.SourcePort, s.DestinationPort = int32(tcp.SrcPort), int32(tcp.DstPort)
	case udp != nil:
		s.SourcePort, s.DestinationPort = int32(udp.SrcPort), int32(udp.DstPort)
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
		s.Protocol, s.Info = "UDP", fmt.Sprintf("Len=%d", len(udp.Payload))
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
	return fmt.Sprintf("[%s] Seq=%d Ack=%d Win=%d Len=%d",
		tcpFlags(tcp), tcp.Seq, tcp.Ack, tcp.Window, len(tcp.Payload))
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
	off := 0
	for _, l := range pkt.Layers() {
		layerLen := len(l.LayerContents())

		switch v := l.(type) {
		case *layers.Ethernet:
			out = append(out, Layer{Name: "Ethernet", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("Destination", v.DstMAC.String(), off, 6),
				newFieldAt("Source", v.SrcMAC.String(), off+6, 6),
				newFieldAt("Type", v.EthernetType.String(), off+12, 2),
			}})

		case *layers.ARP:
			out = append(out, arpLayer(v, off, layerLen))
		case *layers.Dot1Q:
			out = append(out, Layer{Name: "802.1Q VLAN", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("VLAN ID", fmt.Sprintf("%d", v.VLANIdentifier), off, 2),
				newFieldAt("Priority", fmt.Sprintf("%d", v.Priority), off, 2),
				newFieldAt("Type", v.Type.String(), off+2, 2),
			}})

		case *layers.IPv4:
			out = append(out, ipv4Layer(v, off, layerLen))
		case *layers.IPv6:
			out = append(out, Layer{Name: "IPv6", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("Source", v.SrcIP.String(), off+8, 16),
				newFieldAt("Destination", v.DstIP.String(), off+24, 16),
				newFieldAt("Next Header", v.NextHeader.String(), off+6, 1),
				newFieldAt("Hop Limit", fmt.Sprintf("%d", v.HopLimit), off+7, 1),
				newFieldAt("Payload Length", fmt.Sprintf("%d", v.Length), off+4, 2),
			}})

		case *layers.ICMPv4:
			out = append(out, Layer{Name: "ICMP", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("Type/Code", v.TypeCode.String(), off, 2),
				newFieldAt("Checksum", fmt.Sprintf("0x%04x", v.Checksum), off+2, 2),
				newFieldAt("Identifier", fmt.Sprintf("%d", v.Id), off+4, 2),
				newFieldAt("Sequence", fmt.Sprintf("%d", v.Seq), off+6, 2),
			}})

		case *layers.ICMPv6:
			out = append(out, Layer{Name: "ICMPv6", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("Type/Code", v.TypeCode.String(), off, 2),
				newFieldAt("Checksum", fmt.Sprintf("0x%04x", v.Checksum), off+2, 2),
			}})

		case *layers.TCP:
			out = append(out, tcpLayer(v, off, layerLen))
			if (v.SrcPort == bgpPort || v.DstPort == bgpPort) && len(v.Payload) > 0 {
				if msgs := decodeBGP(v.Payload); len(msgs) > 0 {
					payloadOff := off + layerLen
					for _, m := range msgs {
						shiftFields(m.Fields, payloadOff)
						out = append(out, Layer{
							Name: "BGP · " + m.Type, Fields: m.Fields,
							Offset: payloadOff + m.Offset, Length: m.Length,
						})
					}

					bgpDone = true
				}
			}

		case *layers.UDP:
			out = append(out, Layer{Name: "UDP", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("Source Port", fmt.Sprintf("%d", v.SrcPort), off, 2),
				newFieldAt("Destination Port", fmt.Sprintf("%d", v.DstPort), off+2, 2),
				newFieldAt("Length", fmt.Sprintf("%d", v.Length), off+4, 2),
				newFieldAt("Checksum", fmt.Sprintf("0x%04x", v.Checksum), off+6, 2),
			}})

		case *layers.VXLAN:
			out = append(out, Layer{Name: "VXLAN", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("VNI", fmt.Sprintf("%d", v.VNI), off+4, 3),
				newFieldAt("Flags", fmt.Sprintf("0x%02x", flagByte(v.ValidIDFlag)), off, 1),
			}})

		case *gopacket.Payload:
			if bgpDone {
				continue
			}

			out = append(out, Layer{Name: "Data", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("Length", fmt.Sprintf("%d bytes", len(v.Payload())), off, layerLen),
			}})

		case gopacket.ErrorLayer:
			out = append(out, Layer{Name: "Undecoded", Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("Length", fmt.Sprintf("%d bytes", len(v.LayerContents())), off, layerLen),
			}})

		default:
			out = append(out, Layer{Name: l.LayerType().String(), Offset: off, Length: layerLen, Fields: []Field{
				newFieldAt("Length", fmt.Sprintf("%d bytes", len(l.LayerContents())), off, layerLen),
			}})
		}

		off += layerLen
	}

	return out
}

func arpLayer(v *layers.ARP, off, length int) Layer {
	op := "request"
	if v.Operation == layers.ARPReply {
		op = "reply"
	}

	return Layer{Name: "ARP", Offset: off, Length: length, Fields: []Field{
		newFieldAt("Operation", op, off+6, 2),
		newFieldAt("Sender MAC", fmt.Sprintf("% x", v.SourceHwAddress), off+8, 6),
		newFieldAt("Sender IP", fmt.Sprintf("%d.%d.%d.%d", v.SourceProtAddress[0], v.SourceProtAddress[1], v.SourceProtAddress[2], v.SourceProtAddress[3]), off+14, 4),
		newFieldAt("Target MAC", fmt.Sprintf("% x", v.DstHwAddress), off+18, 6),
		newFieldAt("Target IP", fmt.Sprintf("%d.%d.%d.%d", v.DstProtAddress[0], v.DstProtAddress[1], v.DstProtAddress[2], v.DstProtAddress[3]), off+24, 4),
	}}
}

func ipv4Layer(v *layers.IPv4, off, length int) Layer {
	return Layer{Name: "IPv4", Offset: off, Length: length, Fields: []Field{
		newFieldAt("Source", v.SrcIP.String(), off+12, 4),
		newFieldAt("Destination", v.DstIP.String(), off+16, 4),
		newFieldAt("Protocol", v.Protocol.String(), off+9, 1),
		newFieldAt("TTL", fmt.Sprintf("%d", v.TTL), off+8, 1),
		newFieldAt("Identification", fmt.Sprintf("0x%04x", v.Id), off+4, 2),
		newFieldAt("Flags", v.Flags.String(), off+6, 2),
		newFieldAt("Total Length", fmt.Sprintf("%d", v.Length), off+2, 2),
		newFieldAt("Checksum", fmt.Sprintf("0x%04x", v.Checksum), off+10, 2),
	}}
}

func tcpLayer(v *layers.TCP, off, length int) Layer {
	return Layer{Name: "TCP", Offset: off, Length: length, Fields: []Field{
		newFieldAt("Source Port", fmt.Sprintf("%d", v.SrcPort), off, 2),
		newFieldAt("Destination Port", fmt.Sprintf("%d", v.DstPort), off+2, 2),
		newFieldAt("Sequence", fmt.Sprintf("%d", v.Seq), off+4, 4),
		newFieldAt("Acknowledgment", fmt.Sprintf("%d", v.Ack), off+8, 4),
		newFieldAt("Flags", tcpFlags(v), off+12, 2),
		newFieldAt("Window", fmt.Sprintf("%d", v.Window), off+14, 2),
		newFieldAt("Checksum", fmt.Sprintf("0x%04x", v.Checksum), off+16, 2),
		// The payload length is derived, so the field highlights the
		// payload bytes themselves (empty payload keeps no range).
		newFieldAt("Payload Length", fmt.Sprintf("%d", len(v.Payload)), off+length, len(v.Payload)),
	}}
}

func flagByte(valid bool) int {
	if valid {
		return 0x08
	}

	return 0
}
