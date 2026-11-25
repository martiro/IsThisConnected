package main

import (
	"net"
	"testing"
)

func TestEchoRequestChecksum(t *testing.T) {
	p := echoRequest(0x1234, 7)
	if p[0] != icmpEchoRequest || len(p) != 40 || icmpChecksum(p) != 0 { // a valid packet sums to zero
		t.Fatalf("bad echo request % x", p)
	}
}

func TestParseReply(t *testing.T) {
	ipHdr := func(payload []byte) []byte { // minimal IPv4 header (ihl=5), as macOS prepends it
		h := make([]byte, 20)
		h[0] = 0x45
		return append(h, payload...)
	}
	reply := echoRequest(0x1234, 7)
	reply[0] = icmpEchoReply
	timeExceeded := append([]byte{icmpTimeExceeded, 0, 0, 0, 0, 0, 0, 0}, ipHdr(echoRequest(0x1234, 7)[:8])...)
	cases := []struct {
		name    string
		pkt     []byte
		checkID bool
		typ     byte
		ok      bool
	}{
		{"linux echo reply", reply, false, icmpEchoReply, true},
		{"macOS echo reply with IP header", ipHdr(reply), true, icmpEchoReply, true},
		{"reply to another probe", echoRequestAsReply(0x1234, 8), false, icmpEchoReply, false},
		{"reply to another socket (macOS)", echoRequestAsReply(0x9999, 7), true, icmpEchoReply, false},
		{"time exceeded quoting our probe", ipHdr(timeExceeded), true, icmpTimeExceeded, true},
		{"truncated", []byte{0, 0, 0}, false, 0, false},
	}
	for _, c := range cases {
		typ, ok := parseReply(c.pkt, 0x1234, 7, c.checkID)
		if ok != c.ok || (ok && typ != c.typ) {
			t.Errorf("%s: got type %d ok %v", c.name, typ, ok)
		}
	}
}

func echoRequestAsReply(id, seq uint16) []byte {
	p := echoRequest(id, seq)
	p[0] = icmpEchoReply
	return p
}

func TestParseProcRoute(t *testing.T) {
	s := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0100A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"wlan0\t0001A8C0\t00000000\t0001\t0\t0\t600\t00FFFFFF\t0\t0\t0\n"
	gw, iface, err := parseProcRoute(s)
	if err != nil || iface != "eth0" || !gw.Equal(net.ParseIP("192.168.0.1")) {
		t.Fatalf("got %v %q %v, want 192.168.0.1 on eth0 (lowest metric)", gw, iface, err)
	}
	if _, _, err := parseProcRoute(s[:60]); err == nil {
		t.Error("no default route must be an error")
	}
}

func TestParseProcStat(t *testing.T) {
	idle, total, ok := parseProcStat("cpu  100 5 50 800 20 3 2 0 0 0\ncpu0 1 2 3 4 5 6 7 8\n")
	if !ok || idle != 820 || total != 980 {
		t.Fatalf("got idle %d total %d ok %v", idle, total, ok)
	}
}

func TestParseDisconnectLog(t *testing.T) {
	cases := []struct {
		log   string
		code  int
		where string
	}{
		{"wlan0: CTRL-EVENT-DISCONNECTED bssid=02:00:00:00:00:01 reason=3 locally_generated=1", 3, "Your PC (Windows)"},
		{"wlan0: CTRL-EVENT-DISCONNECTED bssid=02:00:00:00:00:01 reason=4 locally_generated=1", 4, "WiFi adapter or access point"},
		{"other line\nwlan0: CTRL-EVENT-DISCONNECTED bssid=02:00:00:00:00:01 reason=2 locally_generated=1\n" +
			"wlan0: CTRL-EVENT-DISCONNECTED bssid=02:00:00:00:00:01 reason=5", 5, "Access point"}, // the last one wins
	}
	for _, c := range cases {
		code, reason, found := parseDisconnectLog(c.log)
		in := Incident{Diagnosis: "wifi_disconnect", Reason: reason}
		in.describe()
		if !found || code != c.code || in.Where != c.where {
			t.Errorf("%q: code %d reason %q where %q", c.log, code, reason, in.Where)
		}
	}
	if _, _, found := parseDisconnectLog("nothing here"); found {
		t.Error("found a reason in an unrelated log")
	}
}

func TestFrequencyHelpers(t *testing.T) {
	for f, want := range map[int][2]any{2412: {1, "2.4 GHz"}, 2437: {6, "2.4 GHz"}, 2484: {14, "2.4 GHz"},
		5180: {36, "5 GHz"}, 5220: {44, "5 GHz"}, 5955: {1, "6 GHz"}, 6115: {33, "6 GHz"}} {
		if ch, band := channelOfMHz(f), bandOfMHz(f); ch != want[0] || band != want[1] {
			t.Errorf("%d MHz: channel %d band %q, want %v", f, ch, band, want)
		}
	}
	if q := qualityOf(-60); q != 80 || qualityOf(-20) != 100 || qualityOf(-110) != 0 {
		t.Errorf("qualityOf(-60) = %v", q)
	}
}
