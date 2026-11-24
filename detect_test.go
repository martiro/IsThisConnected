package main

import (
	"math"
	"net"
	"testing"
)

var nan = math.NaN()

func TestLossJitter(t *testing.T) {
	loss, jit := lossJitter([]float64{10, 20, nan, 30, 40})
	if loss != 20 {
		t.Errorf("loss = %v, want 20", loss)
	}
	if jit != 10 { // pairs (10,20) and (30,40); the gap breaks the chain
		t.Errorf("jitter = %v, want 10", jit)
	}
	if l, j := lossJitter(nil); l != 0 || j != 0 {
		t.Errorf("empty window: %v %v", l, j)
	}
	if l, _ := lossJitter([]float64{nan, nan}); l != 100 {
		t.Errorf("all lost = %v", l)
	}
}

func TestMedianPercentile(t *testing.T) {
	if m := median([]float64{3, 1, 2, 10}); m != 2.5 {
		t.Errorf("median = %v", m)
	}
	if p := percentile([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, 0.95); p != 19 {
		t.Errorf("p95 = %v", p)
	}
}

func good(ts int64) Tick { return Tick{TS: ts, Viol: map[string]float64{}} }

func slow(ts int64, k string, v float64, f Flags) Tick {
	return Tick{TS: ts, Viol: map[string]float64{k: v}, Flags: f}
}

func TestHysteresis(t *testing.T) {
	d := &Detector{OpenAfter: 5, CloseAfter: 15}
	ts := int64(0)
	// 4 bad ticks then a good one: no incident (must be consecutive)
	for i := 0; i < 4; i++ {
		ts += 1000
		if o, _ := d.Step(slow(ts, "rtt_gateway", 80, Flags{GwBad: true})); o != nil {
			t.Fatal("opened too early")
		}
	}
	ts += 1000
	d.Step(good(ts))
	// 5 consecutive bad ticks: opens, start = first bad tick
	var opened *Incident
	first := ts + 1000
	for i := 0; i < 5; i++ {
		ts += 1000
		if o, _ := d.Step(slow(ts, "rtt_gateway", float64(60+i*10), Flags{GwBad: true})); o != nil {
			opened = o
		}
	}
	if opened == nil || opened.Start != first || opened.Trigger != "rtt_gateway" {
		t.Fatalf("opened = %+v", opened)
	}
	lastBad := ts
	// 14 good ticks: still open; 15th closes with End = last bad tick
	for i := 0; i < 14; i++ {
		ts += 1000
		if _, c := d.Step(good(ts)); c != nil {
			t.Fatal("closed too early")
		}
	}
	ts += 1000
	_, closed := d.Step(good(ts))
	if closed == nil || closed.End != lastBad || closed.Peak != 100 {
		t.Fatalf("closed = %+v", closed)
	}
	if closed.Diagnosis != "ap_congestion" || closed.Type != "slowdown" {
		t.Errorf("diagnosis/type = %s/%s", closed.Diagnosis, closed.Type)
	}
}

func TestBadTickResetsCloseCounter(t *testing.T) {
	d := &Detector{OpenAfter: 1, CloseAfter: 3}
	d.Step(slow(1, "dns_system", 500, Flags{DnsBad: true}))
	d.Step(good(2))
	d.Step(good(3))
	d.Step(slow(4, "rtt_8.8.8.8", 200, Flags{Inet1Bad: true})) // merges into same incident
	d.Step(good(5))
	d.Step(good(6))
	_, c := d.Step(good(7))
	if c == nil || len(c.Metrics) != 2 || c.End != 4 {
		t.Fatalf("closed = %+v", c)
	}
}

func TestDisconnectionSequence(t *testing.T) {
	// simulated WiFi off for 30 s: wifi state down + gateway missing + internet loss
	d := &Detector{OpenAfter: 5, CloseAfter: 15}
	var opened, closed *Incident
	ts := int64(0)
	for i := 0; i < 30; i++ {
		ts += 1000
		tk := Tick{TS: ts, Disconnected: true,
			Viol:  map[string]float64{"wifi_state": 0, "no_gateway": 0, "loss_8.8.8.8": 100},
			Flags: Flags{WifiDown: true, Inet1Bad: true, Inet2Bad: true}}
		if o, _ := d.Step(tk); o != nil {
			opened = o
		}
	}
	for i := 0; i < 15; i++ {
		ts += 1000
		if _, c := d.Step(good(ts)); c != nil {
			closed = c
		}
	}
	if opened == nil || closed == nil {
		t.Fatal("incident not detected")
	}
	if closed.Type != "disconnection" || closed.Diagnosis != "wifi_disconnect" || closed.Trigger != "wifi_state" {
		t.Errorf("got %s / %s / %s", closed.Type, closed.Diagnosis, closed.Trigger)
	}
	if closed.End-closed.Start != 29000 {
		t.Errorf("duration = %d", closed.End-closed.Start)
	}
}

func TestDiagnose(t *testing.T) {
	cases := []struct {
		f    Flags
		want string
	}{
		{Flags{WifiDown: true, GwBad: true}, "wifi_disconnect"},
		{Flags{Roamed: true, GwBad: true}, "roaming"},
		{Flags{GwBad: true, RssiDrop: true, Inet1Bad: true}, "weak_signal"},
		{Flags{GwBad: true}, "ap_congestion"},
		{Flags{Inet1Bad: true, Inet2Bad: true}, "isp_wan"},
		{Flags{DnsBad: true}, "dns"},
		{Flags{LinkDrop: true}, "link_degraded"},
		{Flags{TcpBad: true}, "tcp_slow"},
		{Flags{TcpBad: true, Inet1Bad: true}, "single_host"},
		{Flags{DlLow: true}, "low_bandwidth"},
		{Flags{RssiDrop: true}, "other"}, // RSSI alone explains nothing
		{Flags{PcBusy: true, GwBad: true}, "pc_overload"},
		{Flags{Wired: true, GwBad: true, RssiDrop: true}, "wired_lan"},
		{Flags{Inet1Bad: true, Inet2Bad: true, IspKnown: true, IspBad: true}, "isp_wan"},
		{Flags{Inet1Bad: true, Inet2Bad: true, IspKnown: true}, "internet_upstream"},
		{Flags{IspKnown: true, IspBad: true}, "other"}, // a slow ISP router alone is not a fault (ping deprioritized)
	}
	for _, c := range cases {
		if got := diagnose(c.f); got != c.want {
			t.Errorf("diagnose(%+v) = %s, want %s", c.f, got, c.want)
		}
	}
}

func TestISPHop(t *testing.T) {
	ip := net.ParseIP
	gw, dst := ip("192.168.1.254"), ip("8.8.8.8")
	cases := []struct {
		hops []net.IP
		want net.IP
	}{
		{[]net.IP{ip("192.168.1.253"), ip("203.0.113.41"), ip("10.20.0.1")}, ip("203.0.113.41")}, // real-world shape: private hop, ISP, private ISP core
		{[]net.IP{ip("192.168.1.1"), nil, ip("100.64.0.1")}, ip("100.64.0.1")},                   // silent hop, CGNAT = ISP
		{[]net.IP{gw, ip("10.0.0.1"), dst}, nil},                                                 // destination reached first
		{nil, nil},
	}
	for _, c := range cases {
		if got := ispHop(c.hops, gw, dst); !got.Equal(c.want) {
			t.Errorf("ispHop(%v) = %v, want %v", c.hops, got, c.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct{ diag, reason, where string }{
		{"ap_congestion", "", "Access point / router"},
		{"wifi_disconnect", "The network is disconnected due to a temporary-disconnect request.", "Your PC (Windows)"},
		{"wifi_disconnect", "Failed to connect because no connectable Access Point was visible", "Access point"},
		{"wifi_disconnect", "The network is disconnected by the driver.", "WiFi adapter or access point"},
		{"no_such_diagnosis", "", "Unknown"},
	}
	for _, c := range cases {
		in := Incident{Diagnosis: c.diag, Reason: c.reason}
		in.describe()
		if in.Where != c.where || in.Label == "" || in.Why == "" {
			t.Errorf("describe(%s, %q) = %q / %q / %q", c.diag, c.reason, in.Label, in.Where, in.Why)
		}
	}
	for k := range diagInfo { // every diagnosis must be explained
		in := Incident{Diagnosis: k}
		in.describe()
		if in.Why == "" {
			t.Errorf("%s has no explanation", k)
		}
	}
}
