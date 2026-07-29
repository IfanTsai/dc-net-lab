package capture

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()

	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("parse prefix %s: %v", s, err)
	}

	return p
}

func serialize(t *testing.T, ls ...gopacket.SerializableLayer) []byte {
	t.Helper()

	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, ls...); err != nil {
		t.Fatalf("serialize: %v", err)
	}

	return buf.Bytes()
}

func tcpFrame(t *testing.T, src, dst string, srcPort, dstPort uint16) []byte {
	t.Helper()

	ip := &layers.IPv4{
		Version: 4, TTL: 64, Protocol: layers.IPProtocolTCP,
		SrcIP: net.ParseIP(src), DstIP: net.ParseIP(dst),
	}
	tcp := &layers.TCP{SrcPort: layers.TCPPort(srcPort), DstPort: layers.TCPPort(dstPort)}
	if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatalf("checksum: %v", err)
	}

	return serialize(t,
		&layers.Ethernet{
			SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{2, 0, 0, 0, 0, 2},
			EthernetType: layers.EthernetTypeIPv4,
		},
		ip, tcp, gopacket.Payload("hi"))
}

func udpFrame(t *testing.T, src, dst string, srcPort, dstPort uint16) []byte {
	t.Helper()

	ip := &layers.IPv4{
		Version: 4, TTL: 64, Protocol: layers.IPProtocolUDP,
		SrcIP: net.ParseIP(src), DstIP: net.ParseIP(dst),
	}
	udp := &layers.UDP{SrcPort: layers.UDPPort(srcPort), DstPort: layers.UDPPort(dstPort)}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatalf("checksum: %v", err)
	}

	return serialize(t,
		&layers.Ethernet{
			SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{2, 0, 0, 0, 0, 2},
			EthernetType: layers.EthernetTypeIPv4,
		},
		ip, udp, gopacket.Payload("hi"))
}

func icmpFrame(t *testing.T) []byte {
	t.Helper()

	return serialize(t,
		&layers.Ethernet{
			SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{2, 0, 0, 0, 0, 2},
			EthernetType: layers.EthernetTypeIPv4,
		},
		&layers.IPv4{
			Version: 4, TTL: 64, Protocol: layers.IPProtocolICMPv4,
			SrcIP: net.ParseIP("10.0.0.1"), DstIP: net.ParseIP("10.0.0.2"),
		},
		&layers.ICMPv4{TypeCode: layers.CreateICMPv4TypeCode(layers.ICMPv4TypeEchoRequest, 0)})
}

func arpFrame(t *testing.T) []byte {
	t.Helper()

	return serialize(t,
		&layers.Ethernet{
			SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{255, 255, 255, 255, 255, 255},
			EthernetType: layers.EthernetTypeARP,
		},
		&layers.ARP{
			AddrType: layers.LinkTypeEthernet, Protocol: layers.EthernetTypeIPv4,
			HwAddressSize: 6, ProtAddressSize: 4, Operation: layers.ARPRequest,
			SourceHwAddress: []byte{2, 0, 0, 0, 0, 1}, SourceProtAddress: []byte{10, 0, 0, 1},
			DstHwAddress: []byte{0, 0, 0, 0, 0, 0}, DstProtAddress: []byte{10, 0, 0, 2},
		})
}

func TestFilterMatch(t *testing.T) {
	tests := []struct {
		name   string
		filter Filter
		frame  func(*testing.T) []byte
		want   bool
	}{
		{"empty matches tcp", Filter{}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 80)
		}, true},
		{"empty matches arp", Filter{}, arpFrame, true},
		{"proto tcp matches tcp", Filter{Protocol: ProtoTCP}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 80)
		}, true},
		{"proto tcp rejects udp", Filter{Protocol: ProtoTCP}, func(t *testing.T) []byte {
			return udpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 53)
		}, false},
		{"proto udp matches udp", Filter{Protocol: ProtoUDP}, func(t *testing.T) []byte {
			return udpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 53)
		}, true},
		{"proto icmp matches echo", Filter{Protocol: ProtoICMP}, icmpFrame, true},
		{"proto icmp rejects tcp", Filter{Protocol: ProtoICMP}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 80)
		}, false},
		{"proto arp matches arp", Filter{Protocol: ProtoARP}, arpFrame, true},
		{"proto arp rejects ip", Filter{Protocol: ProtoARP}, icmpFrame, false},
		{"proto bgp matches dst 179", Filter{Protocol: ProtoBGP}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.1.0.1", "10.1.0.2", 33000, 179)
		}, true},
		{"proto bgp matches src 179", Filter{Protocol: ProtoBGP}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.1.0.2", "10.1.0.1", 179, 33000)
		}, true},
		{"proto bgp rejects http", Filter{Protocol: ProtoBGP}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 80)
		}, false},
		{"proto vxlan matches 4789", Filter{Protocol: ProtoVXLAN}, func(t *testing.T) []byte {
			return udpFrame(t, "10.2.0.1", "10.2.0.2", 50000, 4789)
		}, true},
		{"proto vxlan rejects dns", Filter{Protocol: ProtoVXLAN}, func(t *testing.T) []byte {
			return udpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 53)
		}, false},
		{"src prefix hit", Filter{Src: mustPrefix(t, "10.100.1.0/24")}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.100.1.11", "10.100.3.11", 1234, 8080)
		}, true},
		{"src prefix miss", Filter{Src: mustPrefix(t, "10.100.1.0/24")}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.100.2.11", "10.100.3.11", 1234, 8080)
		}, false},
		{"dst prefix hit", Filter{Dst: mustPrefix(t, "10.100.3.0/24")}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.100.1.11", "10.100.3.11", 1234, 8080)
		}, true},
		{"dst host prefix miss", Filter{Dst: mustPrefix(t, "10.100.3.12/32")}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.100.1.11", "10.100.3.11", 1234, 8080)
		}, false},
		{"addr filter rejects arp", Filter{Src: mustPrefix(t, "10.0.0.0/8")}, arpFrame, false},
		{"port hit either side", Filter{Port: 8080}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.100.1.11", "10.100.3.11", 8080, 33000)
		}, true},
		{"port miss", Filter{Port: 8080}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.100.1.11", "10.100.3.11", 1234, 80)
		}, false},
		{"port filter rejects icmp", Filter{Port: 8080}, icmpFrame, false},
		{"proto and port combined", Filter{Protocol: ProtoTCP, Port: 80}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 80)
		}, true},
		{"proto hit port miss", Filter{Protocol: ProtoTCP, Port: 81}, func(t *testing.T) []byte {
			return tcpFrame(t, "10.0.0.1", "10.0.0.2", 1234, 80)
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.Match(tt.frame(t)); got != tt.want {
				t.Errorf("Match() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterValidate(t *testing.T) {
	if err := (Filter{Protocol: "gre"}).validate(); err == nil {
		t.Error("unknown protocol accepted")
	}

	if err := (Filter{Protocol: ProtoBGP}).validate(); err != nil {
		t.Errorf("bgp rejected: %v", err)
	}
}

func TestOptionsNormalise(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{"defaults applied", Options{Iface: "eth1"}, false},
		{"missing iface", Options{}, true},
		{"snap too small", Options{Iface: "eth1", SnapLen: 32}, true},
		{"negative duration", Options{Iface: "eth1", Duration: -time.Second}, true},
		{"bad direction", Options{Iface: "eth1", Direction: "in"}, true},
		{"rx ok", Options{Iface: "eth1", Direction: DirectionRx}, false},
		{"bad filter", Options{Iface: "eth1", Filter: Filter{Protocol: "gre"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.normalise()
			if (err != nil) != tt.wantErr {
				t.Errorf("normalise() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	opts := Options{Iface: "eth1"}
	if err := opts.normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}

	if opts.SnapLen != 256 || opts.Duration != 30*time.Second || opts.Direction != DirectionBoth {
		t.Errorf("defaults = %d/%v/%s, want 256/30s/both", opts.SnapLen, opts.Duration, opts.Direction)
	}
}

func TestDirectionAllowed(t *testing.T) {
	tests := []struct {
		direction string
		pkttype   uint8
		want      bool
	}{
		{DirectionBoth, 0, true},
		{DirectionBoth, linuxPacketOutgoing, true},
		{DirectionRx, 0, true},
		{DirectionRx, linuxPacketOutgoing, false},
		{DirectionTx, linuxPacketOutgoing, true},
		{DirectionTx, 0, false},
	}

	for _, tt := range tests {
		if got := directionAllowed(tt.direction, tt.pkttype); got != tt.want {
			t.Errorf("directionAllowed(%s, %d) = %v, want %v", tt.direction, tt.pkttype, got, tt.want)
		}
	}
}
