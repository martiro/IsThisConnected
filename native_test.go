package main

import (
	"net"
	"testing"
	"time"
)

// Sanity checks of the hand-computed Windows struct offsets against the real machine.
func TestNativeAPIs(t *testing.T) {
	if testing.Short() {
		t.Skip("native")
	}
	gw, idx, err := bestRoute(net.ParseIP("8.8.8.8"))
	if err != nil {
		t.Skipf("no route: %v", err)
	}
	t.Logf("gateway %s ifIndex %d", gw, idx)
	in1, out1, typ, err := ifEntry(idx)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	in2, out2, _, _ := ifEntry(idx)
	t.Logf("ifType %d, in %d (+%d) out %d (+%d)", typ, in1, in2-in1, out1, out2-out1)
	if in1 == 0 || in2 < in1 {
		t.Error("implausible InOctets: offset wrong?")
	}
	c, err := newICMP()
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []net.IP{gw, net.ParseIP("8.8.8.8")} {
		rtt, ok := c.ping(ip, 900)
		t.Logf("ping %s: %v ms ok=%v", ip, rtt, ok)
	}
	var hops []net.IP
	dst := net.ParseIP("8.8.8.8")
	for ttl := byte(1); ttl <= 15; ttl++ {
		from, rtt, reached := c.echo(dst, ttl, 1000)
		t.Logf("hop %d: %v %v ms reached=%v", ttl, from, rtt, reached)
		hops = append(hops, from)
		if reached {
			break
		}
	}
	t.Logf("ISP hop: %v", ispHop(hops, gw, dst))
	if hops[0] == nil {
		t.Error("TTL 1 got no answer: IP_OPTION_INFORMATION layout wrong?")
	}
	i1, t1, _ := cpuTimes()
	time.Sleep(500 * time.Millisecond)
	i2, t2, ok := cpuTimes()
	t.Logf("cpu %.1f%% ok=%v", 100-float64(i2-i1)*100/float64(t2-t1), ok)
	if w, err := openWlan(); err == nil {
		wi, err := w.info()
		t.Logf("wifi %+v err=%v", wi, err)
		if wi.Connected && (wi.RSSI < -100 || wi.RSSI > 0 || wi.RxMbps <= 0 || wi.SSID == "" || wi.Band == "") {
			t.Error("implausible WLAN values: offset wrong?")
		}
	}
	m, ok := isMetered()
	t.Logf("metered=%v ok=%v", m, ok)
	code, reason, found := lastDisconnectReason(30 * 24 * time.Hour)
	t.Logf("last wlan event: %d %q found=%v", code, reason, found)
}
