package main

// Pure helpers for the Linux and macOS native layer: ICMP packets, /proc files, wpa_supplicant log lines.
// They have no build tag so their tests run on every platform, including Windows.

import (
	"encoding/binary"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

const (
	icmpEchoReply    = 0
	icmpEchoRequest  = 8
	icmpTimeExceeded = 11
)

// echoRequest builds an ICMP echo request with a 32-byte payload and a valid checksum.
func echoRequest(id, seq uint16) []byte {
	p := make([]byte, 8, 40)
	p[0] = icmpEchoRequest
	binary.BigEndian.PutUint16(p[4:], id)
	binary.BigEndian.PutUint16(p[6:], seq)
	p = append(p, "isthisconnected-probe-payload-32"...)
	binary.BigEndian.PutUint16(p[2:], icmpChecksum(p))
	return p
}

func icmpChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// stripIPv4 removes a leading IPv4 header if present: macOS ping sockets deliver it, Linux ones do not.
// An ICMP message never starts with 0x4_ (no ICMP type in 64-79 is used), so the check is unambiguous.
func stripIPv4(b []byte) []byte {
	if len(b) >= 20 && b[0]>>4 == 4 {
		if ihl := int(b[0]&0x0f) * 4; ihl >= 20 && len(b) >= ihl {
			return b[ihl:]
		}
	}
	return b
}

// parseReply classifies an ICMP message received for our probe (id, seq):
// an echo reply, or a time-exceeded error that quotes our echo request (traceroute hop).
// checkID is false on Linux, where the kernel rewrites the id of ping-socket packets.
func parseReply(b []byte, id, seq uint16, checkID bool) (typ byte, ok bool) {
	b = stripIPv4(b)
	if len(b) < 8 {
		return 0, false
	}
	match := func(m []byte) bool {
		return binary.BigEndian.Uint16(m[6:]) == seq && (!checkID || binary.BigEndian.Uint16(m[4:]) == id)
	}
	switch b[0] {
	case icmpEchoReply:
		return icmpEchoReply, match(b)
	case icmpTimeExceeded: // header(8) + quoted IPv4 header + first 8 bytes of our echo request
		q := stripIPv4(b[8:])
		if len(q) >= 8 && q[0] == icmpEchoRequest && len(q) < len(b[8:]) {
			return icmpTimeExceeded, match(q)
		}
	}
	return 0, false
}

// parseProcRoute returns the default gateway and its interface from /proc/net/route (lowest metric wins).
func parseProcRoute(s string) (gw net.IP, iface string, err error) {
	best := -1
	for _, line := range strings.Split(s, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, _ := strconv.ParseUint(f[3], 16, 32)
		metric, _ := strconv.Atoi(f[6])
		g, err := strconv.ParseUint(f[2], 16, 32)
		if err != nil || flags&0x3 != 0x3 || (best >= 0 && metric >= best) { // RTF_UP|RTF_GATEWAY
			continue
		}
		ip := make(net.IP, 4)
		binary.LittleEndian.PutUint32(ip, uint32(g)) // stored in host (little-endian) order
		gw, iface, best = ip, f[0], metric
	}
	if gw == nil {
		return nil, "", fmt.Errorf("no default route")
	}
	return gw, iface, nil
}

// parseProcStat returns idle (idle+iowait) and total CPU time from the first line of /proc/stat.
func parseProcStat(s string) (idle, total uint64, ok bool) {
	f := strings.Fields(strings.SplitN(s, "\n", 2)[0])
	if len(f) < 9 || f[0] != "cpu" {
		return 0, 0, false
	}
	var v [8]uint64 // user nice system idle iowait irq softirq steal
	for i := range v {
		v[i], _ = strconv.ParseUint(f[i+1], 10, 64)
		total += v[i]
	}
	return v[3] + v[4], total, true
}

var ieeeReasons = map[int]string{
	1: "unspecified", 2: "previous authentication no longer valid", 3: "station is leaving",
	4: "inactivity", 5: "access point overloaded", 6: "frame from a non-authenticated station",
	7: "frame from a non-associated station", 8: "station is leaving the network", 14: "message integrity failure",
	15: "4-way handshake timeout", 16: "group key handshake timeout", 23: "802.1X authentication failed",
	34: "poor channel conditions",
}

var reDisconnect = regexp.MustCompile(`CTRL-EVENT-DISCONNECTED .*reason=(\d+)( locally_generated=1)?`)

// parseDisconnectLog reads the last wpa_supplicant disconnect line and states who dropped the link.
// The wording feeds Incident.describe: "this PC" -> Your PC, "access point" -> Access point.
func parseDisconnectLog(s string) (code int, reason string, found bool) {
	m := reDisconnect.FindAllStringSubmatch(s, -1)
	if m == nil {
		return 0, "", false
	}
	last := m[len(m)-1]
	code, _ = strconv.Atoi(last[1])
	what := ieeeReasons[code]
	if what == "" {
		what = "code " + last[1]
	}
	switch {
	case last[2] != "" && code == 3: // the PC chose to leave: user, sleep, network change
		reason = "Disconnected by this PC (802.11 reason: " + what + ")"
	case last[2] != "": // the PC gave up, e.g. it stopped hearing the access point
		reason = "Connection lost: the WiFi adapter gave up (802.11 reason: " + what + ")"
	default:
		reason = "Disconnected by the access point (802.11 reason: " + what + ")"
	}
	return code, reason, true
}
