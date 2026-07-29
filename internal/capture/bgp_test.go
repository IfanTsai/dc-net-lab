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

func field(t *testing.T, m bgpMessage, name string) string {
	t.Helper()

	for _, f := range m.Fields {
		if f.Name == name {
			return f.Value
		}
	}

	t.Fatalf("message %s has no field %q (%+v)", m.Type, name, m.Fields)

	return ""
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
