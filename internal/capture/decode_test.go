package capture

import (
	"bytes"
	"net"
	"strings"
	"testing"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

func serialize(t *testing.T, ls ...gopacket.SerializableLayer) []byte {
	t.Helper()

	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, ls...); err != nil {
		t.Fatalf("serialize: %v", err)
	}

	return buf.Bytes()
}

func eth() *layers.Ethernet {
	return &layers.Ethernet{
		SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{2, 0, 0, 0, 0, 2},
		EthernetType: layers.EthernetTypeIPv4,
	}
}

func tcpFrame(t *testing.T, srcPort, dstPort uint16, payload []byte) []byte {
	t.Helper()

	ip := &layers.IPv4{
		Version: 4, TTL: 64, Protocol: layers.IPProtocolTCP,
		SrcIP: net.ParseIP("10.0.0.1"), DstIP: net.ParseIP("10.0.0.2"),
	}

	tcp := &layers.TCP{SrcPort: layers.TCPPort(srcPort), DstPort: layers.TCPPort(dstPort), PSH: true, ACK: true}
	if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatalf("checksum: %v", err)
	}

	return serialize(t, eth(), ip, tcp, gopacket.Payload(payload))
}

func TestSummarizeTCP(t *testing.T) {
	s := Summarize(tcpFrame(t, 33000, 8080, []byte("hello")))

	if s.Source != "10.0.0.1" || s.Destination != "10.0.0.2" {
		t.Errorf("addresses: %s → %s", s.Source, s.Destination)
	}

	if s.Protocol != "TCP" {
		t.Errorf("protocol: %s", s.Protocol)
	}

	if !strings.Contains(s.Info, "PSH, ACK") || !strings.Contains(s.Info, "Len=5") {
		t.Errorf("info: %s", s.Info)
	}

	if strings.Contains(s.Info, "33000") || strings.Contains(s.Info, "8080") {
		t.Errorf("info should not repeat the ports (they have their own columns): %s", s.Info)
	}

	if s.SourcePort != 33000 || s.DestinationPort != 8080 {
		t.Errorf("ports: %d → %d", s.SourcePort, s.DestinationPort)
	}
}

func TestSummarizeBGPKeepalive(t *testing.T) {
	s := Summarize(tcpFrame(t, 33000, 179, bgpRaw(4, nil)))

	if s.Protocol != "BGP" {
		t.Errorf("protocol: %s", s.Protocol)
	}

	if s.Info != "KEEPALIVE" {
		t.Errorf("info: %s", s.Info)
	}
}

func TestSummarizeBGPPortWithoutPayload(t *testing.T) {
	// A bare ACK on port 179 is TCP, not BGP.
	s := Summarize(tcpFrame(t, 179, 33000, nil))

	if s.Protocol != "TCP" {
		t.Errorf("protocol: %s", s.Protocol)
	}
}

func TestSummarizeARP(t *testing.T) {
	frame := serialize(t,
		&layers.Ethernet{
			SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{255, 255, 255, 255, 255, 255},
			EthernetType: layers.EthernetTypeARP,
		},
		&layers.ARP{
			AddrType: layers.LinkTypeEthernet, Protocol: layers.EthernetTypeIPv4,
			HwAddressSize: 6, ProtAddressSize: 4, Operation: layers.ARPRequest,
			SourceHwAddress: []byte{2, 0, 0, 0, 0, 1}, SourceProtAddress: []byte{10, 100, 0, 11},
			DstHwAddress: []byte{0, 0, 0, 0, 0, 0}, DstProtAddress: []byte{10, 100, 0, 1},
		})

	s := Summarize(frame)
	if s.Protocol != "ARP" {
		t.Errorf("protocol: %s", s.Protocol)
	}

	if s.Info != "Who has 10.100.0.1? Tell 10.100.0.11" {
		t.Errorf("info: %s", s.Info)
	}
}

func TestSummarizeICMP(t *testing.T) {
	frame := serialize(t, eth(),
		&layers.IPv4{
			Version: 4, TTL: 64, Protocol: layers.IPProtocolICMPv4,
			SrcIP: net.ParseIP("10.100.0.11"), DstIP: net.ParseIP("10.100.1.11"),
		},
		&layers.ICMPv4{TypeCode: layers.CreateICMPv4TypeCode(layers.ICMPv4TypeEchoRequest, 0), Id: 7, Seq: 3})

	s := Summarize(frame)
	if s.Protocol != "ICMP" {
		t.Errorf("protocol: %s", s.Protocol)
	}

	if !strings.Contains(s.Info, "id=7 seq=3") {
		t.Errorf("info: %s", s.Info)
	}
}

func TestSummarizeVXLANInner(t *testing.T) {
	innerIP := &layers.IPv4{
		Version: 4, TTL: 64, Protocol: layers.IPProtocolICMPv4,
		SrcIP: net.ParseIP("172.16.0.1"), DstIP: net.ParseIP("172.16.0.2"),
	}

	outerIP := &layers.IPv4{
		Version: 4, TTL: 64, Protocol: layers.IPProtocolUDP,
		SrcIP: net.ParseIP("10.2.0.1"), DstIP: net.ParseIP("10.2.0.2"),
	}

	udp := &layers.UDP{SrcPort: 50000, DstPort: 4789}
	if err := udp.SetNetworkLayerForChecksum(outerIP); err != nil {
		t.Fatalf("checksum: %v", err)
	}

	frame := serialize(t, eth(), outerIP, udp,
		&layers.VXLAN{ValidIDFlag: true, VNI: 42},
		eth(), innerIP,
		&layers.ICMPv4{TypeCode: layers.CreateICMPv4TypeCode(layers.ICMPv4TypeEchoRequest, 0)})

	s := Summarize(frame)
	if s.Source != "172.16.0.1" || s.Destination != "172.16.0.2" {
		t.Errorf("inner addresses not used: %s → %s", s.Source, s.Destination)
	}

	if s.Protocol != "ICMP" || !strings.Contains(s.Info, "VXLAN VNI=42") {
		t.Errorf("protocol/info: %s / %s", s.Protocol, s.Info)
	}
}

func TestTreeLayers(t *testing.T) {
	tree := Tree(tcpFrame(t, 33000, 179, bgpRaw(4, nil)))

	var names []string
	for _, l := range tree {
		names = append(names, l.Name)
	}

	want := []string{"Ethernet", "IPv4", "TCP", "BGP · KEEPALIVE"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("layers: %v, want %v", names, want)
	}
}

func TestTreePlainPayload(t *testing.T) {
	tree := Tree(tcpFrame(t, 33000, 8080, []byte("hello")))

	last := tree[len(tree)-1]
	if last.Name != "Data" {
		t.Errorf("last layer: %s", last.Name)
	}
}

func treeField(t *testing.T, tree []Layer, layer, name string) Field {
	t.Helper()

	for _, l := range tree {
		if l.Name != layer {
			continue
		}

		for _, f := range l.Fields {
			if f.Name == name {
				return f
			}
		}
	}

	t.Fatalf("layer %s has no field %q", layer, name)

	return Field{}
}

// TestTreeFieldOffsets checks that a few representative fields point at
// the right bytes of the raw frame, which is what the viewer's hex
// highlight sync relies on.
func TestTreeFieldOffsets(t *testing.T) {
	data := tcpFrame(t, 33000, 8080, []byte("hello"))
	tree := Tree(data)

	cases := []struct {
		layer, field string
		want         []byte
	}{
		{"Ethernet", "Source", data[6:12]},
		{"Ethernet", "Destination", data[0:6]},
		{"IPv4", "Source", net.ParseIP("10.0.0.1").To4()},
		{"IPv4", "Destination", net.ParseIP("10.0.0.2").To4()},
		{"TCP", "Source Port", []byte{0x80, 0xe8}}, // 33000
	}

	for _, c := range cases {
		f := treeField(t, tree, c.layer, c.field)
		if f.Length != len(c.want) {
			t.Errorf("%s.%s length = %d, want %d", c.layer, c.field, f.Length, len(c.want))

			continue
		}

		got := data[f.Offset : f.Offset+f.Length]
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s.%s bytes = % x, want % x", c.layer, c.field, got, c.want)
		}
	}
}

// TestTreeBGPFieldOffsets checks that BGP field offsets, produced
// relative to the TCP payload, are shifted frame-absolute by Tree.
func TestTreeBGPFieldOffsets(t *testing.T) {
	data := tcpFrame(t, 33000, 179, bgpRaw(1, bgpOpenBody(65100, 9, [4]byte{10, 1, 0, 1})))
	tree := Tree(data)

	rid := treeField(t, tree, "BGP · OPEN", "Router ID")
	if rid.Length != 4 {
		t.Fatalf("router id length: %d", rid.Length)
	}

	if got := data[rid.Offset : rid.Offset+rid.Length]; !bytes.Equal(got, []byte{10, 1, 0, 1}) {
		t.Errorf("router id bytes: % x", got)
	}

	if typ := treeField(t, tree, "BGP · OPEN", "Message Type"); typ.Value != "OPEN" || typ.Length != 1 {
		t.Errorf("message type: %+v", typ)
	}

	marker := treeField(t, tree, "BGP · OPEN", "Marker")
	if got := data[marker.Offset]; got != 0xff || marker.Length != 16 {
		t.Errorf("marker: byte %x at %d, length %d", got, marker.Offset, marker.Length)
	}
}
