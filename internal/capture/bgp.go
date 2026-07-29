package capture

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
)

// Minimal BGP-4 (RFC 4271) message decoder: gopacket ships no BGP
// layer, and BGP is the platform's core protocol, so the viewer
// decodes message framing, OPEN with its capabilities (RFC 5492),
// UPDATE prefixes with the standard path attributes and NOTIFICATION
// codes with subcode names. AS numbers in AS_PATH are parsed as 4
// octets first (FRR always negotiates the 4-octet capability, and the
// platform's leaf ASNs do not fit 2 octets) with a 2-octet fallback
// when the segment math does not add up.
//
// All field offsets produced here are relative to the start of the
// TCP payload the messages were parsed from; Tree shifts them to
// frame-absolute positions for the hex highlight sync.

const bgpHeaderLen = 19

var bgpTypeNames = map[byte]string{
	1: "OPEN", 2: "UPDATE", 3: "NOTIFICATION", 4: "KEEPALIVE", 5: "ROUTE-REFRESH",
}

var bgpNotificationCodes = map[byte]string{
	1: "Message Header Error", 2: "OPEN Message Error", 3: "UPDATE Message Error",
	4: "Hold Timer Expired", 5: "FSM Error", 6: "Cease",
}

// bgpNotificationSubcodes names the per-code subcodes (RFC 4271 §6,
// RFC 6608 for FSM, RFC 4486 + RFC 8538 for Cease).
var bgpNotificationSubcodes = map[byte]map[byte]string{
	1: {
		1: "Connection Not Synchronized", 2: "Bad Message Length", 3: "Bad Message Type",
	},
	2: {
		1: "Unsupported Version Number", 2: "Bad Peer AS", 3: "Bad BGP Identifier",
		4: "Unsupported Optional Parameter", 6: "Unacceptable Hold Time", 7: "Unsupported Capability",
	},
	3: {
		1: "Malformed Attribute List", 2: "Unrecognized Well-known Attribute", 3: "Missing Well-known Attribute",
		4: "Attribute Flags Error", 5: "Attribute Length Error", 6: "Invalid ORIGIN Attribute",
		8: "Invalid NEXT_HOP Attribute", 9: "Optional Attribute Error", 10: "Invalid Network Field",
		11: "Malformed AS_PATH",
	},
	5: {
		1: "Unexpected Message in OpenSent", 2: "Unexpected Message in OpenConfirm", 3: "Unexpected Message in Established",
	},
	6: {
		1: "Maximum Number of Prefixes Reached", 2: "Administrative Shutdown", 3: "Peer De-configured",
		4: "Administrative Reset", 5: "Connection Rejected", 6: "Other Configuration Change",
		7: "Connection Collision Resolution", 8: "Out of Resources", 9: "Hard Reset",
	},
}

// bgpAttrNames labels every standard path attribute so unknown-to-the-
// decoder attributes still show up in the tree instead of vanishing.
var bgpAttrNames = map[byte]string{
	1: "Origin", 2: "AS Path", 3: "Next Hop", 4: "MED", 5: "Local Pref",
	6: "Atomic Aggregate", 7: "Aggregator", 8: "Communities", 9: "Originator ID",
	10: "Cluster List", 14: "MP Reach NLRI", 15: "MP Unreach NLRI",
	16: "Extended Communities", 17: "AS4 Path", 18: "AS4 Aggregator", 32: "Large Communities",
}

// bgpMessage is one decoded BGP message: its tree layer fields plus
// the one-line summary used in the packet list. Offset/Length locate
// the message within the TCP payload it was parsed from, for the
// viewer's hex highlight sync.
type bgpMessage struct {
	Type    string
	Summary string
	Fields  []Field
	Offset  int
	Length  int
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
	pos := 0
	for len(payload) > 0 {
		if len(payload) < bgpHeaderLen {
			if len(msgs) > 0 {
				msgs = append(msgs, bgpMessage{Type: "?", Summary: "[truncated]", Offset: pos, Length: len(payload)})
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
			msgs = append(msgs, bgpMessage{
				Type: name, Summary: name + " [truncated]", Offset: pos, Length: len(payload),
				Fields: []Field{newField("Length", fmt.Sprintf("%d (only %d captured)", length, len(payload)))},
			})

			return msgs
		}

		msg := decodeBGPMessage(name, payload[bgpHeaderLen:length], pos+bgpHeaderLen)
		msg.Offset, msg.Length = pos, length
		msg.Fields = append(bgpHeaderFields(pos, length, name), msg.Fields...)
		msgs = append(msgs, msg)

		payload = payload[length:]
		pos += length
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

// bgpHeaderFields are the fixed 19-byte message header lines shared
// by every message type. The type field is named "Message Type" so
// the viewer's name-keyed tooltips don't collide with Ethernet's
// EtherType "Type" field.
func bgpHeaderFields(pos, length int, name string) []Field {
	return []Field{
		newFieldAt("Marker", "ff…ff", pos, 16),
		newFieldAt("Length", fmt.Sprintf("%d", length), pos+16, 2),
		newFieldAt("Message Type", name, pos+18, 1),
	}
}

// decodeBGPMessage decodes the body of one message; base is the
// body's offset within the TCP payload, so every field can carry its
// byte range.
func decodeBGPMessage(name string, body []byte, base int) bgpMessage {
	switch name {
	case "OPEN":
		return decodeBGPOpen(body, base)
	case "UPDATE":
		return decodeBGPUpdate(body, base)
	case "NOTIFICATION":
		return decodeBGPNotification(body, base)
	case "ROUTE-REFRESH":
		return decodeBGPRouteRefresh(body, base)
	default:
		return bgpMessage{Type: name, Summary: name}
	}
}

func decodeBGPOpen(body []byte, base int) bgpMessage {
	m := bgpMessage{Type: "OPEN", Summary: "OPEN"}
	if len(body) < 10 {
		m.Summary = "OPEN [truncated]"

		return m
	}

	asn := binary.BigEndian.Uint16(body[1:3])
	hold := binary.BigEndian.Uint16(body[3:5])
	routerID := netip.AddrFrom4([4]byte(body[5:9]))
	optLen := int(body[9])

	asText := fmt.Sprintf("%d", asn)
	if asn == 23456 {
		asText += " (AS_TRANS, 4-octet AS in capability)"
	}

	m.Fields = []Field{
		newFieldAt("Version", fmt.Sprintf("%d", body[0]), base, 1),
		newFieldAt("AS", asText, base+1, 2),
		newFieldAt("Hold Time", fmt.Sprintf("%d s", hold), base+3, 2),
		newFieldAt("Router ID", routerID.String(), base+5, 4),
	}

	summaryAS := asText
	if optLen > 0 && len(body) >= 10+optLen {
		params, realAS := bgpOpenParams(body[10:10+optLen], base+10)
		opt := newFieldAt("Optional Parameters", fmt.Sprintf("%d bytes", optLen), base+10, optLen)
		opt.Children = params
		m.Fields = append(m.Fields, opt)

		if realAS != 0 {
			summaryAS = fmt.Sprintf("%d", realAS)
			if asn == 23456 {
				asText += fmt.Sprintf(", real AS %d", realAS)
				m.Fields[1].Value = asText
			}
		}
	}

	m.Summary = fmt.Sprintf("OPEN AS %s hold %ds", summaryAS, hold)

	return m
}

// bgpOpenParams walks the OPEN optional parameters; parameter type 2
// (Capabilities, RFC 5492) is expanded into one field per capability.
// realAS is the 4-octet AS from capability 65 when present.
func bgpOpenParams(buf []byte, base int) (fields []Field, realAS uint32) {
	pos := 0
	for pos+2 <= len(buf) {
		ptype, plen := buf[pos], int(buf[pos+1])
		if pos+2+plen > len(buf) {
			return fields, realAS
		}

		value := buf[pos+2 : pos+2+plen]
		if ptype == 2 {
			caps, as := bgpCapabilities(value, base+pos+2)
			fields = append(fields, caps...)
			if as != 0 {
				realAS = as
			}
		} else {
			fields = append(fields, newFieldAt(fmt.Sprintf("Parameter %d", ptype), fmt.Sprintf("%d bytes", plen), base+pos, 2+plen))
		}

		pos += 2 + plen
	}

	return fields, realAS
}

func bgpCapabilities(buf []byte, base int) (fields []Field, realAS uint32) {
	pos := 0
	for pos+2 <= len(buf) {
		code, clen := buf[pos], int(buf[pos+1])
		if pos+2+clen > len(buf) {
			return fields, realAS
		}

		value := buf[pos+2 : pos+2+clen]
		name, text := bgpCapability(code, value)
		fields = append(fields, newFieldAt(name, text, base+pos, 2+clen))

		if code == 65 && clen == 4 {
			realAS = binary.BigEndian.Uint32(value)
		}

		pos += 2 + clen
	}

	return fields, realAS
}

// bgpCapability renders one capability (IANA "Capability Codes").
func bgpCapability(code byte, v []byte) (name, text string) {
	switch code {
	case 1:
		if len(v) == 4 {
			return "Multiprotocol", afiSafiName(binary.BigEndian.Uint16(v[:2]), v[3])
		}

		return "Multiprotocol", "?"
	case 2:
		return "Route Refresh", "supported"
	case 5:
		return "Extended Next Hop", bgpExtNextHop(v)
	case 6:
		return "Extended Message", "supported"
	case 64:
		return "Graceful Restart", bgpGracefulRestart(v)
	case 65:
		if len(v) == 4 {
			return "4-octet AS", fmt.Sprintf("%d", binary.BigEndian.Uint32(v))
		}

		return "4-octet AS", "?"
	case 69:
		return "ADD-PATH", bgpAddPath(v)
	case 70:
		return "Enhanced Route Refresh", "supported"
	case 71:
		return "Long-Lived Graceful Restart", "supported"
	case 73:
		return "FQDN", bgpFQDN(v)
	case 128:
		return "Route Refresh (Cisco)", "supported"
	default:
		return fmt.Sprintf("Capability %d", code), fmt.Sprintf("%d bytes", len(v))
	}
}

var bgpAFINames = map[uint16]string{1: "IPv4", 2: "IPv6", 25: "L2VPN"}

var bgpSAFINames = map[byte]string{
	1: "Unicast", 2: "Multicast", 4: "Labeled Unicast", 70: "EVPN", 128: "MPLS VPN",
}

func afiSafiName(afi uint16, safi byte) string {
	a, aok := bgpAFINames[afi]
	s, sok := bgpSAFINames[safi]
	if !aok || !sok {
		return fmt.Sprintf("AFI %d / SAFI %d", afi, safi)
	}

	return a + " " + s
}

func bgpExtNextHop(v []byte) string {
	var parts []string
	for len(v) >= 6 {
		afi := binary.BigEndian.Uint16(v[:2])
		safi := byte(binary.BigEndian.Uint16(v[2:4]))
		nh := binary.BigEndian.Uint16(v[4:6])
		parts = append(parts, fmt.Sprintf("%s via %s", afiSafiName(afi, safi), bgpAFINames[nh]))

		v = v[6:]
	}

	if len(parts) == 0 {
		return "?"
	}

	return strings.Join(parts, ", ")
}

func bgpGracefulRestart(v []byte) string {
	if len(v) < 2 {
		return "?"
	}

	head := binary.BigEndian.Uint16(v[:2])
	text := fmt.Sprintf("restart time %d s", head&0x0fff)
	if head&0x8000 != 0 {
		text += " (restarted)"
	}

	var families []string
	for rest := v[2:]; len(rest) >= 4; rest = rest[4:] {
		families = append(families, afiSafiName(binary.BigEndian.Uint16(rest[:2]), rest[2]))
	}

	if len(families) > 0 {
		text += ": " + strings.Join(families, ", ")
	}

	return text
}

func bgpAddPath(v []byte) string {
	modes := map[byte]string{1: "RX", 2: "TX", 3: "TX+RX"}

	var parts []string
	for len(v) >= 4 {
		mode := modes[v[3]]
		if mode == "" {
			mode = "?"
		}

		parts = append(parts, fmt.Sprintf("%s %s", afiSafiName(binary.BigEndian.Uint16(v[:2]), v[2]), mode))

		v = v[4:]
	}

	if len(parts) == 0 {
		return "?"
	}

	return strings.Join(parts, ", ")
}

func bgpFQDN(v []byte) string {
	if len(v) < 1 || len(v) < 1+int(v[0]) {
		return "?"
	}

	host := string(v[1 : 1+int(v[0])])
	rest := v[1+int(v[0]):]
	if len(rest) >= 1 && len(rest) >= 1+int(rest[0]) && rest[0] > 0 {
		host += "." + string(rest[1:1+int(rest[0])])
	}

	return host
}

func decodeBGPNotification(body []byte, base int) bgpMessage {
	m := bgpMessage{Type: "NOTIFICATION", Summary: "NOTIFICATION"}
	if len(body) < 2 {
		return m
	}

	code := bgpNotificationCodes[body[0]]
	if code == "" {
		code = fmt.Sprintf("code %d", body[0])
	}

	subText := fmt.Sprintf("%d", body[1])
	summary := "NOTIFICATION " + code
	if sub := bgpNotificationSubcodes[body[0]][body[1]]; sub != "" {
		subText = fmt.Sprintf("%s (%d)", sub, body[1])
		summary += ": " + sub
	}

	m.Summary = summary
	m.Fields = []Field{
		newFieldAt("Error", code, base, 1),
		newFieldAt("Subcode", subText, base+1, 1),
	}

	return m
}

func decodeBGPRouteRefresh(body []byte, base int) bgpMessage {
	m := bgpMessage{Type: "ROUTE-REFRESH", Summary: "ROUTE-REFRESH"}
	if len(body) < 4 {
		return m
	}

	family := afiSafiName(binary.BigEndian.Uint16(body[:2]), body[3])
	m.Summary = "ROUTE-REFRESH " + family
	m.Fields = []Field{newFieldAt("Family", family, base, 4)}

	return m
}

func decodeBGPUpdate(body []byte, base int) bgpMessage {
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

	attrsBase := base + 2 + len(withdrawn) + 2
	withdrawnFields := bgpPrefixFields(withdrawn, base+2)
	attrFields := bgpAttrFields(attrs, attrsBase)
	nlriFields := bgpPrefixFields(nlriBytes, attrsBase+len(attrs))
	withdrawnPrefixes := fieldValues(withdrawnFields)
	nlri := fieldValues(nlriFields)

	var fields []Field
	if len(attrFields) > 0 {
		f := newFieldAt("Path Attributes", fmt.Sprintf("%d attributes", len(attrFields)), attrsBase-2, 2+len(attrs))
		f.Children = attrFields
		fields = append(fields, f)
	}

	if len(nlriFields) > 0 {
		f := newFieldAt("NLRI", joinCapped(nlri), attrsBase+len(attrs), len(nlriBytes))
		f.Children = nlriFields
		fields = append(fields, f)
	}

	if len(withdrawnFields) > 0 {
		f := newFieldAt("Withdrawn Routes", joinCapped(withdrawnPrefixes), base, 2+len(withdrawn))
		f.Children = withdrawnFields
		fields = append(fields, f)
	}

	m.Fields = fields

	switch {
	case len(nlri) == 0 && len(withdrawnPrefixes) == 0 && len(attrs) == 0:
		m.Summary = "UPDATE (End-of-RIB)"
	case len(withdrawnPrefixes) > 0 && len(nlri) == 0:
		m.Summary = fmt.Sprintf("UPDATE withdraw %s", joinCapped(withdrawnPrefixes))
	default:
		m.Summary = fmt.Sprintf("UPDATE %s", joinCapped(nlri))
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

// bgpPrefixFields parses the packed IPv4 prefixes of NLRI / withdrawn
// blocks into one field per prefix, stopping quietly at malformed
// input.
func bgpPrefixFields(buf []byte, base int) []Field {
	var out []Field
	pos := 0
	for pos < len(buf) {
		bits := int(buf[pos])
		n := (bits + 7) / 8
		if bits > 32 || pos+1+n > len(buf) {
			return out
		}

		var addr [4]byte
		copy(addr[:], buf[pos+1:pos+1+n])
		out = append(out, newFieldAt("Prefix", fmt.Sprintf("%s/%d", netip.AddrFrom4(addr), bits), base+pos, 1+n))

		pos += 1 + n
	}

	return out
}

// fieldValues extracts the value strings for summary building.
func fieldValues(fields []Field) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Value)
	}

	return out
}

// bgpAttrFields renders every path attribute as one field spanning
// the attribute's full byte range (header included). Attributes the
// decoder has no renderer for still appear with their name (or type
// number) and byte size, so nothing silently disappears from the
// tree.
func bgpAttrFields(attrs []byte, base int) []Field {
	var fields []Field
	pos := 0
	for len(attrs)-pos >= 3 {
		flags, typ := attrs[pos], attrs[pos+1]

		var length, hdr int
		if flags&0x10 != 0 { // extended length
			if len(attrs)-pos < 4 {
				return fields
			}

			length, hdr = int(binary.BigEndian.Uint16(attrs[pos+2:pos+4])), 4
		} else {
			length, hdr = int(attrs[pos+2]), 3
		}

		if len(attrs)-pos < hdr+length {
			return fields
		}

		value := attrs[pos+hdr : pos+hdr+length]
		name := bgpAttrNames[typ]
		if name == "" {
			name = fmt.Sprintf("Attribute %d", typ)
		}

		fields = append(fields, newFieldAt(name, bgpAttrValue(typ, value), base+pos, hdr+length))

		pos += hdr + length
	}

	return fields
}

// bgpAttrValue renders one path attribute value; unknown types fall
// back to their byte size.
func bgpAttrValue(typ byte, v []byte) string {
	switch typ {
	case 1:
		// ORIGIN
		return bgpOrigin(v)
	case 2:
		// AS_PATH
		return bgpASPath(v)
	case 3, 9: // NEXT_HOP, ORIGINATOR_ID
		if len(v) == 4 {
			return netip.AddrFrom4([4]byte(v)).String()
		}

	case 4, 5: // MULTI_EXIT_DISC, LOCAL_PREF
		if len(v) == 4 {
			return fmt.Sprintf("%d", binary.BigEndian.Uint32(v))
		}

	case 6:
		// ATOMIC_AGGREGATE
		return "present"
	case 7, 18:
		// AGGREGATOR, AS4_AGGREGATOR
		return bgpAggregator(v)
	case 8:
		// COMMUNITIES
		return bgpCommunities(v)
	case 10:
		// CLUSTER_LIST
		return bgpClusterList(v)
	case 16:
		// EXTENDED COMMUNITIES
		return bgpExtCommunities(v)
	case 17: // AS4_PATH (always 4-octet by definition)
		if path, ok := bgpASPathSized(v, 4); ok {
			return path
		}

		return "?"
	case 32:
		// LARGE COMMUNITIES
		return bgpLargeCommunities(v)
	}

	return fmt.Sprintf("%d bytes", len(v))
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

// bgpAggregator renders AGGREGATOR: 4-octet AS + IPv4 (8 bytes) or
// the legacy 2-octet AS form (6 bytes).
func bgpAggregator(v []byte) string {
	switch len(v) {
	case 8:
		return fmt.Sprintf("AS %d, %s", binary.BigEndian.Uint32(v[:4]), netip.AddrFrom4([4]byte(v[4:8])))
	case 6:
		return fmt.Sprintf("AS %d, %s", binary.BigEndian.Uint16(v[:2]), netip.AddrFrom4([4]byte(v[2:6])))
	default:
		return "?"
	}
}

// bgpWellKnownCommunities per RFC 1997 / RFC 8326.
var bgpWellKnownCommunities = map[uint32]string{
	0xffff0000: "GRACEFUL_SHUTDOWN", 0xffffff01: "NO_EXPORT",
	0xffffff02: "NO_ADVERTISE", 0xffffff03: "NO_EXPORT_SUBCONFED",
}

func bgpCommunities(v []byte) string {
	var parts []string
	for ; len(v) >= 4; v = v[4:] {
		c := binary.BigEndian.Uint32(v[:4])
		if name := bgpWellKnownCommunities[c]; name != "" {
			parts = append(parts, name)

			continue
		}

		parts = append(parts, fmt.Sprintf("%d:%d", c>>16, c&0xffff))
	}

	if len(parts) == 0 {
		return "?"
	}

	return strings.Join(parts, " ")
}

func bgpLargeCommunities(v []byte) string {
	var parts []string
	for ; len(v) >= 12; v = v[12:] {
		parts = append(parts, fmt.Sprintf("%d:%d:%d",
			binary.BigEndian.Uint32(v[:4]), binary.BigEndian.Uint32(v[4:8]), binary.BigEndian.Uint32(v[8:12])))
	}

	if len(parts) == 0 {
		return "?"
	}

	return strings.Join(parts, " ")
}

// bgpExtCommunities renders the transitive two-octet-AS and IPv4
// route-target/route-origin forms; anything else shows as hex.
func bgpExtCommunities(v []byte) string {
	var parts []string
	for ; len(v) >= 8; v = v[8:] {
		typ, sub := v[0]&0x3f, v[1]
		switch {
		case typ == 0x00 && sub == 0x02:
			parts = append(parts, fmt.Sprintf("RT:%d:%d", binary.BigEndian.Uint16(v[2:4]), binary.BigEndian.Uint32(v[4:8])))
		case typ == 0x00 && sub == 0x03:
			parts = append(parts, fmt.Sprintf("RO:%d:%d", binary.BigEndian.Uint16(v[2:4]), binary.BigEndian.Uint32(v[4:8])))
		case typ == 0x01 && sub == 0x02:
			parts = append(parts, fmt.Sprintf("RT:%s:%d", netip.AddrFrom4([4]byte(v[2:6])), binary.BigEndian.Uint16(v[6:8])))
		case typ == 0x02 && sub == 0x02:
			parts = append(parts, fmt.Sprintf("RT:%d:%d", binary.BigEndian.Uint32(v[2:6]), binary.BigEndian.Uint16(v[6:8])))
		default:
			parts = append(parts, fmt.Sprintf("0x%x", v[:8]))
		}
	}

	if len(parts) == 0 {
		return "?"
	}

	return strings.Join(parts, " ")
}

func bgpClusterList(v []byte) string {
	var parts []string
	for ; len(v) >= 4; v = v[4:] {
		parts = append(parts, netip.AddrFrom4([4]byte(v[:4])).String())
	}

	if len(parts) == 0 {
		return "?"
	}

	return strings.Join(parts, " ")
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

// joinCapped joins up to three items, summarising the rest.
func joinCapped(items []string) string {
	const n = 3
	if len(items) <= n {
		return strings.Join(items, ", ")
	}

	return fmt.Sprintf("%s +%d more", strings.Join(items[:n], ", "), len(items)-n)
}
