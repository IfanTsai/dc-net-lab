package capture

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
)

// Minimal BGP-4 (RFC 4271) message decoder: gopacket ships no BGP
// layer, and BGP is the platform's core protocol, so the viewer
// decodes message framing, OPEN essentials, UPDATE prefixes with the
// key path attributes and NOTIFICATION codes. AS numbers in AS_PATH
// are parsed as 4 octets first (FRR always negotiates the 4-octet
// capability, and the platform's leaf ASNs do not fit 2 octets) with
// a 2-octet fallback when the segment math does not add up.

const bgpHeaderLen = 19

var bgpTypeNames = map[byte]string{
	1: "OPEN", 2: "UPDATE", 3: "NOTIFICATION", 4: "KEEPALIVE", 5: "ROUTE-REFRESH",
}

var bgpNotificationCodes = map[byte]string{
	1: "Message Header Error", 2: "OPEN Message Error", 3: "UPDATE Message Error",
	4: "Hold Timer Expired", 5: "FSM Error", 6: "Cease",
}

// bgpMessage is one decoded BGP message: its tree layer fields plus
// the one-line summary used in the packet list.
type bgpMessage struct {
	Type    string
	Summary string
	Fields  []Field
}

// bgpInfo joins message summaries into the list row info column.
func bgpInfo(msgs []bgpMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.Summary)
	}

	return strings.Join(parts, ", ")
}

// decodeBGP parses as many complete BGP messages as the payload holds
// (several often share one TCP segment). A message cut off by TCP
// segmentation or the snap length is reported as truncated; leading
// garbage (e.g. the tail of a message started in a previous segment)
// stops the parse and yields nothing.
func decodeBGP(payload []byte) []bgpMessage {
	var msgs []bgpMessage
	for len(payload) > 0 {
		if len(payload) < bgpHeaderLen {
			if len(msgs) > 0 {
				msgs = append(msgs, bgpMessage{Type: "?", Summary: "[truncated]"})
			}

			return msgs
		}

		if !validMarker(payload[:16]) {
			return msgs
		}

		length := int(binary.BigEndian.Uint16(payload[16:18]))
		typ := payload[18]
		name, ok := bgpTypeNames[typ]
		if !ok || length < bgpHeaderLen || length > 4096 {
			return msgs
		}

		if length > len(payload) {
			msgs = append(msgs, bgpMessage{Type: name, Summary: name + " [truncated]", Fields: []Field{
				{"Length", fmt.Sprintf("%d (only %d captured)", length, len(payload))},
			}})

			return msgs
		}

		msgs = append(msgs, decodeBGPMessage(name, payload[bgpHeaderLen:length]))
		payload = payload[length:]
	}

	return msgs
}

func validMarker(marker []byte) bool {
	for _, b := range marker {
		if b != 0xff {
			return false
		}
	}

	return true
}

func decodeBGPMessage(name string, body []byte) bgpMessage {
	switch name {
	case "OPEN":
		return decodeBGPOpen(body)
	case "UPDATE":
		return decodeBGPUpdate(body)
	case "NOTIFICATION":
		return decodeBGPNotification(body)
	default:
		return bgpMessage{Type: name, Summary: name}
	}
}

func decodeBGPOpen(body []byte) bgpMessage {
	m := bgpMessage{Type: "OPEN", Summary: "OPEN"}
	if len(body) < 9 {
		m.Summary = "OPEN [truncated]"

		return m
	}

	asn := binary.BigEndian.Uint16(body[1:3])
	hold := binary.BigEndian.Uint16(body[3:5])
	routerID := netip.AddrFrom4([4]byte(body[5:9]))

	asText := fmt.Sprintf("%d", asn)
	if asn == 23456 {
		asText += " (AS_TRANS, 4-octet AS in capability)"
	}

	m.Summary = fmt.Sprintf("OPEN AS %s hold %ds", asText, hold)
	m.Fields = []Field{
		{"Version", fmt.Sprintf("%d", body[0])},
		{"AS", asText},
		{"Hold Time", fmt.Sprintf("%d s", hold)},
		{"Router ID", routerID.String()},
	}

	return m
}

func decodeBGPNotification(body []byte) bgpMessage {
	m := bgpMessage{Type: "NOTIFICATION", Summary: "NOTIFICATION"}
	if len(body) < 2 {
		return m
	}

	code := bgpNotificationCodes[body[0]]
	if code == "" {
		code = fmt.Sprintf("code %d", body[0])
	}

	m.Summary = "NOTIFICATION " + code
	m.Fields = []Field{
		{"Error", code},
		{"Subcode", fmt.Sprintf("%d", body[1])},
	}

	return m
}

func decodeBGPUpdate(body []byte) bgpMessage {
	m := bgpMessage{Type: "UPDATE", Summary: "UPDATE"}

	withdrawn, rest, ok := bgpBlock(body)
	if !ok {
		m.Summary = "UPDATE [truncated]"

		return m
	}

	attrs, nlriBytes, ok := bgpBlock(rest)
	if !ok {
		m.Summary = "UPDATE [truncated]"

		return m
	}

	withdrawnPrefixes := bgpPrefixes(withdrawn)
	nlri := bgpPrefixes(nlriBytes)
	fields := bgpAttrFields(attrs)

	if len(nlri) > 0 {
		fields = append(fields, Field{"NLRI", strings.Join(nlri, ", ")})
	}

	if len(withdrawnPrefixes) > 0 {
		fields = append(fields, Field{"Withdrawn", strings.Join(withdrawnPrefixes, ", ")})
	}

	m.Fields = fields

	switch {
	case len(nlri) == 0 && len(withdrawnPrefixes) == 0 && len(attrs) == 0:
		m.Summary = "UPDATE (End-of-RIB)"
	case len(withdrawnPrefixes) > 0 && len(nlri) == 0:
		m.Summary = fmt.Sprintf("UPDATE withdraw %s", joinCapped(withdrawnPrefixes, 3))
	default:
		m.Summary = fmt.Sprintf("UPDATE %s", joinCapped(nlri, 3))
	}

	return m
}

// bgpBlock splits a 2-byte-length-prefixed block off buf.
func bgpBlock(buf []byte) (block, rest []byte, ok bool) {
	if len(buf) < 2 {
		return nil, nil, false
	}

	n := int(binary.BigEndian.Uint16(buf[:2]))
	if len(buf) < 2+n {
		return nil, nil, false
	}

	return buf[2 : 2+n], buf[2+n:], true
}

// bgpPrefixes parses the packed IPv4 prefixes of NLRI / withdrawn
// blocks, stopping quietly at malformed input.
func bgpPrefixes(buf []byte) []string {
	var out []string
	for len(buf) > 0 {
		bits := int(buf[0])
		n := (bits + 7) / 8
		if bits > 32 || len(buf) < 1+n {
			return out
		}

		var addr [4]byte
		copy(addr[:], buf[1:1+n])
		out = append(out, fmt.Sprintf("%s/%d", netip.AddrFrom4(addr), bits))

		buf = buf[1+n:]
	}

	return out
}

// bgpAttrFields extracts the well-known path attributes worth showing.
func bgpAttrFields(attrs []byte) []Field {
	var fields []Field
	for len(attrs) >= 3 {
		flags, typ := attrs[0], attrs[1]

		var length, hdr int
		if flags&0x10 != 0 { // extended length
			if len(attrs) < 4 {
				return fields
			}

			length, hdr = int(binary.BigEndian.Uint16(attrs[2:4])), 4
		} else {
			length, hdr = int(attrs[2]), 3
		}

		if len(attrs) < hdr+length {
			return fields
		}

		value := attrs[hdr : hdr+length]
		switch typ {
		case 1: // ORIGIN
			fields = append(fields, Field{"Origin", bgpOrigin(value)})
		case 2: // AS_PATH
			fields = append(fields, Field{"AS Path", bgpASPath(value)})
		case 3: // NEXT_HOP
			if len(value) == 4 {
				fields = append(fields, Field{"Next Hop", netip.AddrFrom4([4]byte(value)).String()})
			}

		case 4: // MULTI_EXIT_DISC
			if len(value) == 4 {
				fields = append(fields, Field{"MED", fmt.Sprintf("%d", binary.BigEndian.Uint32(value))})
			}

		case 5: // LOCAL_PREF
			if len(value) == 4 {
				fields = append(fields, Field{"Local Pref", fmt.Sprintf("%d", binary.BigEndian.Uint32(value))})
			}
		}

		attrs = attrs[hdr+length:]
	}

	return fields
}

func bgpOrigin(v []byte) string {
	if len(v) != 1 {
		return "?"
	}

	switch v[0] {
	case 0:
		return "IGP"
	case 1:
		return "EGP"
	default:
		return "Incomplete"
	}
}

// bgpASPath renders the AS_PATH segments, trying 4-octet AS numbers
// first and falling back to 2-octet when the lengths do not fit.
func bgpASPath(v []byte) string {
	if len(v) == 0 {
		return "(empty)"
	}

	for _, size := range []int{4, 2} {
		if path, ok := bgpASPathSized(v, size); ok {
			return path
		}
	}

	return "?"
}

func bgpASPathSized(v []byte, size int) (string, bool) {
	var parts []string
	for len(v) > 0 {
		if len(v) < 2 {
			return "", false
		}

		segType, count := v[0], int(v[1])
		if len(v) < 2+count*size {
			return "", false
		}

		asns := make([]string, 0, count)
		for i := range count {
			chunk := v[2+i*size : 2+(i+1)*size]

			var asn uint32
			if size == 4 {
				asn = binary.BigEndian.Uint32(chunk)
			} else {
				asn = uint32(binary.BigEndian.Uint16(chunk))
			}

			asns = append(asns, fmt.Sprintf("%d", asn))
		}

		seg := strings.Join(asns, " ")
		if segType == 1 { // AS_SET
			seg = "{" + strings.Join(asns, ",") + "}"
		}

		parts = append(parts, seg)
		v = v[2+count*size:]
	}

	return strings.Join(parts, " "), true
}

// joinCapped joins up to n items, summarising the rest.
func joinCapped(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}

	return fmt.Sprintf("%s +%d more", strings.Join(items[:n], ", "), len(items)-n)
}
