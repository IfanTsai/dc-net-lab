package capture

import (
	"encoding/binary"
	"strings"
	"testing"
)

// bgpRaw frames one BGP message of the given type around a body.
func bgpRaw(typ byte, body []byte) []byte {
	msg := make([]byte, bgpHeaderLen+len(body))
	for i := range 16 {
		msg[i] = 0xff
	}

	binary.BigEndian.PutUint16(msg[16:18], uint16(len(msg)))
	msg[18] = typ
	copy(msg[bgpHeaderLen:], body)

	return msg
}

func bgpOpenBody(asn uint16, hold uint16, routerID [4]byte) []byte {
	body := []byte{4} // version
	body = binary.BigEndian.AppendUint16(body, asn)
	body = binary.BigEndian.AppendUint16(body, hold)
	body = append(body, routerID[:]...)
	body = append(body, 0) // no optional parameters

	return body
}

// bgpUpdateBody builds an UPDATE with origin IGP, a 4-octet AS_PATH,
// a next hop and one NLRI prefix.
func bgpUpdateBody() []byte {
	var attrs []byte
	attrs = append(attrs, 0x40, 1, 1, 0)                       // ORIGIN IGP
	asPath := []byte{2, 2}                                     // AS_SEQUENCE, 2 ASNs
	asPath = binary.BigEndian.AppendUint32(asPath, 65100)      // spine
	asPath = binary.BigEndian.AppendUint32(asPath, 4200080001) // leaf (4-octet only)
	attrs = append(attrs, 0x40, 2, byte(len(asPath)))          // AS_PATH header
	attrs = append(attrs, asPath...)                           //
	attrs = append(attrs, 0x40, 3, 4, 10, 0, 0, 1)             // NEXT_HOP 10.0.0.1
	attrs = append(attrs, 0x80, 5, 4)                          // LOCAL_PREF header
	attrs = binary.BigEndian.AppendUint32(attrs, 100)          //

	body := []byte{0, 0} // no withdrawn routes
	body = binary.BigEndian.AppendUint16(body, uint16(len(attrs)))
	body = append(body, attrs...)
	body = append(body, 24, 10, 100, 1) // NLRI 10.100.1.0/24

	return body
}

// findField searches a field tree (children included) by name.
func findField(fields []Field, name string) (Field, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}

		if c, ok := findField(f.Children, name); ok {
			return c, true
		}
	}

	return Field{}, false
}

func field(t *testing.T, m bgpMessage, name string) string {
	t.Helper()

	f, ok := findField(m.Fields, name)
	if !ok {
		t.Fatalf("message %s has no field %q (%+v)", m.Type, name, m.Fields)
	}

	return f.Value
}

func TestDecodeBGPKeepaliveAndOpen(t *testing.T) {
	payload := append(bgpRaw(4, nil), bgpRaw(1, bgpOpenBody(65100, 9, [4]byte{10, 1, 0, 1}))...)

	msgs := decodeBGP(payload)
	if len(msgs) != 2 {
		t.Fatalf("messages: %d, want 2", len(msgs))
	}

	if msgs[0].Type != "KEEPALIVE" || msgs[1].Type != "OPEN" {
		t.Errorf("types: %s, %s", msgs[0].Type, msgs[1].Type)
	}

	if got := field(t, msgs[1], "Router ID"); got != "10.1.0.1" {
		t.Errorf("router id: %s", got)
	}

	if !strings.Contains(msgs[1].Summary, "AS 65100") {
		t.Errorf("open summary: %s", msgs[1].Summary)
	}
}

func TestDecodeBGPUpdate(t *testing.T) {
	msgs := decodeBGP(bgpRaw(2, bgpUpdateBody()))
	if len(msgs) != 1 {
		t.Fatalf("messages: %d", len(msgs))
	}

	m := msgs[0]
	if m.Type != "UPDATE" {
		t.Fatalf("type: %s", m.Type)
	}

	if got := field(t, m, "NLRI"); got != "10.100.1.0/24" {
		t.Errorf("nlri: %s", got)
	}

	if got := field(t, m, "Next Hop"); got != "10.0.0.1" {
		t.Errorf("next hop: %s", got)
	}

	if got := field(t, m, "AS Path"); got != "65100 4200080001" {
		t.Errorf("as path: %s", got)
	}

	if got := field(t, m, "Local Pref"); got != "100" {
		t.Errorf("local pref: %s", got)
	}

	if !strings.Contains(m.Summary, "10.100.1.0/24") {
		t.Errorf("summary: %s", m.Summary)
	}
}

func TestDecodeBGPWithdraw(t *testing.T) {
	body := []byte{0, 4, 24, 10, 100, 2, 0, 0} // withdraw 10.100.2.0/24, no attrs

	msgs := decodeBGP(bgpRaw(2, body))
	if len(msgs) != 1 {
		t.Fatalf("messages: %d", len(msgs))
	}

	if !strings.Contains(msgs[0].Summary, "withdraw 10.100.2.0/24") {
		t.Errorf("summary: %s", msgs[0].Summary)
	}
}

func TestDecodeBGPEndOfRIB(t *testing.T) {
	msgs := decodeBGP(bgpRaw(2, []byte{0, 0, 0, 0}))
	if len(msgs) != 1 || msgs[0].Summary != "UPDATE (End-of-RIB)" {
		t.Fatalf("messages: %+v", msgs)
	}
}

func TestDecodeBGPNotification(t *testing.T) {
	msgs := decodeBGP(bgpRaw(3, []byte{6, 4}))
	if len(msgs) != 1 || !strings.Contains(msgs[0].Summary, "Cease") {
		t.Fatalf("messages: %+v", msgs)
	}

	if !strings.Contains(msgs[0].Summary, "Administrative Reset") {
		t.Errorf("summary lacks subcode name: %s", msgs[0].Summary)
	}

	if got := field(t, msgs[0], "Subcode"); got != "Administrative Reset (4)" {
		t.Errorf("subcode: %s", got)
	}
}

func TestDecodeBGPNotificationUnknownSubcode(t *testing.T) {
	msgs := decodeBGP(bgpRaw(3, []byte{4, 0}))
	if len(msgs) != 1 || msgs[0].Summary != "NOTIFICATION Hold Timer Expired" {
		t.Fatalf("messages: %+v", msgs)
	}

	if got := field(t, msgs[0], "Subcode"); got != "0" {
		t.Errorf("subcode: %s", got)
	}
}

func TestDecodeBGPTruncated(t *testing.T) {
	full := bgpRaw(2, bgpUpdateBody())

	msgs := decodeBGP(full[:25]) // snapped mid-message
	if len(msgs) != 1 {
		t.Fatalf("messages: %d", len(msgs))
	}

	if !strings.Contains(msgs[0].Summary, "truncated") {
		t.Errorf("summary: %s", msgs[0].Summary)
	}
}

func TestDecodeBGPGarbage(t *testing.T) {
	if msgs := decodeBGP([]byte("definitely not bgp at all, just tcp payload")); len(msgs) != 0 {
		t.Errorf("garbage decoded: %+v", msgs)
	}
}

// bgpOpenBodyWithCaps builds an OPEN body whose optional parameters
// hold one capabilities parameter wrapping the given capability TLVs.
func bgpOpenBodyWithCaps(asn uint16, caps ...[]byte) []byte {
	var capBytes []byte
	for _, c := range caps {
		capBytes = append(capBytes, c...)
	}

	body := []byte{4}
	body = binary.BigEndian.AppendUint16(body, asn)
	body = binary.BigEndian.AppendUint16(body, 180)
	body = append(body, 10, 1, 0, 1)
	body = append(body, byte(2+len(capBytes)), 2, byte(len(capBytes)))
	body = append(body, capBytes...)

	return body
}

func TestDecodeBGPOpenCapabilities(t *testing.T) {
	fourOctet := append([]byte{65, 4}, binary.BigEndian.AppendUint32(nil, 4200080001)...)
	mp := []byte{1, 4, 0, 1, 0, 1}             // IPv4 Unicast
	gr := []byte{64, 6, 0x10, 120, 0, 1, 1, 0} // restart 120s, IPv4 Unicast
	addPath := []byte{69, 4, 0, 1, 1, 3}       // IPv4 Unicast TX+RX
	fqdn := []byte{73, 7, 4, 'l', 'e', 'a', 'f', 1, 'a'}
	unknown := []byte{99, 2, 0, 0}

	msgs := decodeBGP(bgpRaw(1, bgpOpenBodyWithCaps(23456, fourOctet, mp, gr, addPath, fqdn, unknown)))
	if len(msgs) != 1 {
		t.Fatalf("messages: %d", len(msgs))
	}

	m := msgs[0]
	if !strings.Contains(m.Summary, "AS 4200080001") {
		t.Errorf("summary should use the 4-octet AS: %s", m.Summary)
	}

	cases := []struct{ name, want string }{
		{"4-octet AS", "4200080001"},
		{"Multiprotocol", "IPv4 Unicast"},
		{"Graceful Restart", "restart time 120 s: IPv4 Unicast"},
		{"ADD-PATH", "IPv4 Unicast TX+RX"},
		{"FQDN", "leaf.a"},
		{"Capability 99", "2 bytes"},
	}

	for _, c := range cases {
		if got := field(t, m, c.name); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	if got := field(t, m, "AS"); !strings.Contains(got, "real AS 4200080001") {
		t.Errorf("AS field: %s", got)
	}
}

func TestDecodeBGPFieldOffsets(t *testing.T) {
	payload := bgpRaw(1, bgpOpenBody(65100, 9, [4]byte{10, 1, 0, 1}))

	msgs := decodeBGP(payload)
	rid, ok := findField(msgs[0].Fields, "Router ID")
	if !ok {
		t.Fatal("no Router ID field")
	}

	if rid.Offset != bgpHeaderLen+5 || rid.Length != 4 {
		t.Errorf("router id range: [%d, +%d), want [%d, +4)", rid.Offset, rid.Length, bgpHeaderLen+5)
	}

	want := []byte{10, 1, 0, 1}
	if got := payload[rid.Offset : rid.Offset+rid.Length]; string(got) != string(want) {
		t.Errorf("router id bytes: % x, want % x", got, want)
	}
}

func TestDecodeBGPUpdatePrefixOffsets(t *testing.T) {
	payload := bgpRaw(2, bgpUpdateBody())

	msgs := decodeBGP(payload)
	nlri, ok := findField(msgs[0].Fields, "NLRI")
	if !ok || len(nlri.Children) != 1 {
		t.Fatalf("nlri children: %+v", nlri)
	}

	p := nlri.Children[0]
	want := []byte{24, 10, 100, 1}
	if got := payload[p.Offset : p.Offset+p.Length]; string(got) != string(want) {
		t.Errorf("prefix bytes: % x, want % x", got, want)
	}
}

func TestDecodeBGPCommunities(t *testing.T) {
	var attrs []byte
	attrs = append(attrs, 0xc0, 8, 8)                        // COMMUNITIES, 2 entries
	attrs = binary.BigEndian.AppendUint32(attrs, 0xffffff01) // NO_EXPORT
	attrs = binary.BigEndian.AppendUint32(attrs, 65100<<16|100)
	large := []byte{0xc0, 32, 12}
	large = binary.BigEndian.AppendUint32(large, 65100)
	large = binary.BigEndian.AppendUint32(large, 1)
	large = binary.BigEndian.AppendUint32(large, 2)
	attrs = append(attrs, large...)

	body := []byte{0, 0}
	body = binary.BigEndian.AppendUint16(body, uint16(len(attrs)))
	body = append(body, attrs...)
	body = append(body, 24, 10, 100, 1)

	msgs := decodeBGP(bgpRaw(2, body))
	if got := field(t, msgs[0], "Communities"); got != "NO_EXPORT 65100:100" {
		t.Errorf("communities: %s", got)
	}

	if got := field(t, msgs[0], "Large Communities"); got != "65100:1:2" {
		t.Errorf("large communities: %s", got)
	}
}

func TestDecodeBGPUnknownAttribute(t *testing.T) {
	attrs := []byte{0xc0, 99, 3, 1, 2, 3}

	body := []byte{0, 0}
	body = binary.BigEndian.AppendUint16(body, uint16(len(attrs)))
	body = append(body, attrs...)
	body = append(body, 24, 10, 100, 1)

	msgs := decodeBGP(bgpRaw(2, body))
	if got := field(t, msgs[0], "Attribute 99"); got != "3 bytes" {
		t.Errorf("unknown attribute: %s", got)
	}
}

func TestDecodeBGPAggregator(t *testing.T) {
	var attrs []byte
	attrs = append(attrs, 0xc0, 7, 8)
	attrs = binary.BigEndian.AppendUint32(attrs, 4200080001)
	attrs = append(attrs, 10, 1, 0, 1)

	body := []byte{0, 0}
	body = binary.BigEndian.AppendUint16(body, uint16(len(attrs)))
	body = append(body, attrs...)
	body = append(body, 24, 10, 100, 1)

	msgs := decodeBGP(bgpRaw(2, body))
	if got := field(t, msgs[0], "Aggregator"); got != "AS 4200080001, 10.1.0.1" {
		t.Errorf("aggregator: %s", got)
	}
}

func TestDecodeBGPRouteRefresh(t *testing.T) {
	msgs := decodeBGP(bgpRaw(5, []byte{0, 1, 0, 1}))
	if len(msgs) != 1 || msgs[0].Summary != "ROUTE-REFRESH IPv4 Unicast" {
		t.Fatalf("messages: %+v", msgs)
	}
}

func TestDecodeBGPTwoOctetASPath(t *testing.T) {
	var attrs []byte
	asPath := []byte{2, 2}
	asPath = binary.BigEndian.AppendUint16(asPath, 65100)
	asPath = binary.BigEndian.AppendUint16(asPath, 65000)
	attrs = append(attrs, 0x40, 2, byte(len(asPath)))
	attrs = append(attrs, asPath...)

	body := []byte{0, 0}
	body = binary.BigEndian.AppendUint16(body, uint16(len(attrs)))
	body = append(body, attrs...)
	body = append(body, 24, 10, 100, 1)

	msgs := decodeBGP(bgpRaw(2, body))
	if got := field(t, msgs[0], "AS Path"); got != "65100 65000" {
		t.Errorf("as path: %s", got)
	}
}
