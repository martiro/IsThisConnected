package main

// Pure logic (no I/O): window statistics, incident hysteresis and diagnosis.

import (
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
)

// lossJitter: win holds RTTs in ms, NaN = timeout.
// Jitter = mean |rtt[i]-rtt[i-1]| over consecutive successful replies (RFC 3550 style, unsmoothed).
func lossJitter(win []float64) (loss, jitter float64) {
	if len(win) == 0 {
		return 0, 0
	}
	lost, pairs := 0, 0
	sum := 0.0
	for i, v := range win {
		if math.IsNaN(v) {
			lost++
			continue
		}
		if i > 0 && !math.IsNaN(win[i-1]) {
			sum += math.Abs(v - win[i-1])
			pairs++
		}
	}
	if pairs > 0 {
		jitter = sum / float64(pairs)
	}
	return float64(lost) * 100 / float64(len(win)), jitter
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func percentile(s []float64, p float64) float64 { // s must be sorted
	if len(s) == 0 {
		return math.NaN()
	}
	return s[int(math.Ceil(p*float64(len(s))))-1]
}

// Flags describe which hops of the chain PC -> radio -> gateway -> ISP -> Internet -> DNS are degraded.
// PcBusy = CPU saturated; Wired = the active path is Ethernet; IspKnown/IspBad = first ISP hop found / degraded.
type Flags struct {
	WifiDown, Roamed, GwBad, Inet1Bad, Inet2Bad, DnsBad, TcpBad, LinkDrop, RssiDrop, DlLow bool
	PcBusy, Wired, IspKnown, IspBad                                                        bool
}

func (a *Flags) or(b Flags) {
	a.WifiDown = a.WifiDown || b.WifiDown
	a.Roamed = a.Roamed || b.Roamed
	a.GwBad = a.GwBad || b.GwBad
	a.Inet1Bad = a.Inet1Bad || b.Inet1Bad
	a.Inet2Bad = a.Inet2Bad || b.Inet2Bad
	a.DnsBad = a.DnsBad || b.DnsBad
	a.TcpBad = a.TcpBad || b.TcpBad
	a.LinkDrop = a.LinkDrop || b.LinkDrop
	a.RssiDrop = a.RssiDrop || b.RssiDrop
	a.DlLow = a.DlLow || b.DlLow
	a.PcBusy = a.PcBusy || b.PcBusy
	a.Wired = a.Wired || b.Wired
	a.IspKnown = a.IspKnown || b.IspKnown
	a.IspBad = a.IspBad || b.IspBad
}

// diagnose: the first degraded hop closest to the PC explains everything downstream.
func diagnose(f Flags) string {
	switch {
	case f.WifiDown:
		return "wifi_disconnect"
	case f.PcBusy:
		return "pc_overload"
	case f.Roamed:
		return "roaming"
	case f.GwBad && f.Wired:
		return "wired_lan"
	case f.GwBad && f.RssiDrop:
		return "weak_signal"
	case f.GwBad:
		return "ap_congestion"
	case f.Inet1Bad && f.Inet2Bad && f.IspKnown && !f.IspBad:
		return "internet_upstream"
	case f.Inet1Bad && f.Inet2Bad:
		return "isp_wan"
	case f.DnsBad:
		return "dns"
	case f.LinkDrop:
		return "link_degraded"
	case f.TcpBad && !f.Inet1Bad && !f.Inet2Bad:
		return "tcp_slow"
	case f.DlLow:
		return "low_bandwidth"
	case f.Inet1Bad || f.Inet2Bad:
		return "single_host"
	}
	return "other"
}

// Violation keys in trigger priority order (when several start on the same tick).
var violationOrder = []string{
	"wifi_state", "no_gateway", "rtt_gateway", "loss_gateway", "jitter_gateway",
	"rtt_8.8.8.8", "loss_8.8.8.8", "jitter_8.8.8.8", "rtt_1.1.1.1", "loss_1.1.1.1", "jitter_1.1.1.1",
	"dns_system", "dns_8.8.8.8", "tcp_1.1.1.1", "link_rate",
}

type Tick struct {
	TS           int64              // ms
	Viol         map[string]float64 // violated metric -> current value
	Disconnected bool               // always accompanied by its cause in Viol (wifi_state, no_gateway, loss_gateway)
	Flags        Flags
}

type Incident struct {
	ID         int64    `json:"id"`
	Start      int64    `json:"start"`
	End        int64    `json:"end"` // 0 = ongoing
	Type       string   `json:"type"`
	Trigger    string   `json:"trigger"`
	Metrics    []string `json:"metrics"`
	Peak       float64  `json:"peak"`
	Diagnosis  string   `json:"diagnosis"`
	ReasonCode int      `json:"reason_code"`
	Reason     string   `json:"reason"`
	SSID       string   `json:"ssid"`
	BSSID      string   `json:"bssid"`
	Snapshot   string   `json:"snapshot"`
	Label      string   `json:"label"` // computed by describe(), not stored
	Where      string   `json:"where"`
	Why        string   `json:"why"`
	flags      Flags
}

func (in *Incident) addMetric(m string) {
	for _, x := range in.Metrics {
		if x == m {
			return
		}
	}
	in.Metrics = append(in.Metrics, m)
}

// worse: for link rate lower is worse, for everything else higher is worse.
func worse(metric string, a, b float64) bool {
	if metric == "link_rate" {
		return a < b
	}
	return a > b
}

type Detector struct {
	OpenAfter, CloseAfter int
	bad, good             int
	lastBad               int64
	pending               *Incident // streak being evaluated, not yet confirmed
	Cur                   *Incident // confirmed, open incident
}

// Step feeds one tick. It returns the incident when it gets confirmed (opened) or closed.
func (d *Detector) Step(t Tick) (opened, closed *Incident) {
	isBad := len(t.Viol) > 0 || t.Disconnected
	if !isBad {
		if d.Cur == nil {
			d.bad, d.pending = 0, nil
			return nil, nil
		}
		d.good++
		if d.good >= d.CloseAfter {
			closed, d.Cur = d.Cur, nil
			closed.End = d.lastBad
			d.bad, d.good = 0, 0
		}
		return nil, closed
	}
	d.good = 0
	d.bad++
	d.lastBad = t.TS
	in := d.Cur
	if in == nil {
		if d.pending == nil {
			d.pending = &Incident{Start: t.TS, Type: "slowdown"}
			for _, k := range violationOrder {
				if _, ok := t.Viol[k]; ok && d.pending.Trigger == "" {
					d.pending.Trigger = k
				}
			}
			if v, ok := t.Viol[d.pending.Trigger]; ok {
				d.pending.Peak = v
			}
		}
		in = d.pending
	}
	if t.Disconnected {
		in.Type = "disconnection"
	}
	for k, v := range t.Viol {
		in.addMetric(k)
		if k == in.Trigger && worse(k, v, in.Peak) {
			in.Peak = v
		}
	}
	in.flags.or(t.Flags)
	in.Diagnosis = diagnose(in.flags)
	if d.Cur == nil && d.bad >= d.OpenAfter {
		d.Cur, d.pending = in, nil
		opened = in
	}
	return opened, nil
}

// diagInfo: diagnosis key (stored in the DB) -> label, where the problem is, plain-English explanation.
var diagInfo = map[string][3]string{
	"wifi_disconnect": {"WiFi disconnected", "WiFi link", ""}, // where/why refined from the Windows reason
	"pc_overload": {"PC overloaded", "Your PC",
		"The CPU stayed above the limit while the network got slow. The delay is most likely inside this computer (busy CPU, heavy apps, updates), not in the network."},
	"roaming": {"Switching access point", "WiFi link",
		"The PC moved to a different access point of the same network. A short hiccup is normal; if it happens often, the PC sits between two access points with similar signal."},
	"wired_lan": {"Router / LAN problem", "Router / LAN",
		"The PC is on cable and the router itself answered slowly or not at all. WiFi is not involved: suspect the router, the switch or the cable."},
	"weak_signal": {"Weak WiFi signal", "WiFi signal",
		"The router answered slowly and at the same time the WiFi signal dropped by at least the configured dB. Distance, walls or the PC position are the likely cause."},
	"ap_congestion": {"Access point / router slow", "Access point / router",
		"The router answered slowly while the WiFi signal was stable: the air channel is congested (many devices or neighbouring networks) or the access point/router is overloaded. Look at the Access points table: if only one access point has problems, it is that one."},
	"isp_wan": {"ISP / line problem", "ISP / your line",
		"Your router answered fine, but the first router of the ISP and the Internet were slow. The problem is on your line, the modem, or inside the ISP network."},
	"internet_upstream": {"Internet beyond your ISP", "Internet (beyond ISP)",
		"Your router and the first ISP router were fine, but both Internet servers were slow. The problem is further away, between the ISP and the rest of the Internet."},
	"dns": {"DNS slow", "DNS server",
		"Pings were fine but name resolution was slow or failed: websites take long to start loading. Try another DNS server (e.g. 1.1.1.1 or 8.8.8.8)."},
	"link_degraded": {"WiFi link speed dropped", "WiFi link",
		"The speed negotiated between PC and access point fell below half of its usual value (24 h median): weaker modulation, usually due to signal or interference."},
	"tcp_slow": {"Slow connections (ping OK)", "Internet / filtering",
		"Pings were fine but opening a TCP connection was slow: a firewall, proxy or congestion that affects real traffic but not ping."},
	"low_bandwidth": {"Low bandwidth", "ISP or WiFi bandwidth",
		"The download test was far below its usual speed while pings were fine: the available bandwidth was reduced (ISP or WiFi)."},
	"single_host": {"One Internet server slow", "Single remote server",
		"Only one of the two Internet test servers was slow: a problem of that server or its route, not of your connection."},
	"other": {"Other", "Unknown", "The thresholds were exceeded but the pattern does not match a known cause."},
}

// describe fills Label/Where/Why. A WiFi disconnection is attributed using the reason Windows logged.
func (in *Incident) describe() {
	d, ok := diagInfo[in.Diagnosis]
	if !ok {
		d = diagInfo["other"]
	}
	in.Label, in.Where, in.Why = d[0], d[1], d[2]
	if in.Diagnosis != "wifi_disconnect" {
		return
	}
	r := strings.ToLower(in.Reason)
	switch {
	case in.Reason == "":
		in.Why = "The WiFi adapter lost the connection. Windows did not log a reason (yet)."
	case strings.Contains(r, "user") || strings.Contains(r, "this pc") || strings.Contains(r, "temporary-disconnect") || strings.Contains(r, "power"):
		in.Where = "Your PC (Windows)"
		in.Why = fmt.Sprintf("Windows itself closed the connection (%q), e.g. sleep, standby or a manual disconnect. Not a network fault.", in.Reason)
	case strings.Contains(r, "access point") || strings.Contains(r, "not visible"):
		in.Where = "Access point"
		in.Why = fmt.Sprintf("Windows could no longer see the access point (%q): it was switched off, restarted or out of range.", in.Reason)
	default:
		in.Where = "WiFi adapter or access point"
		in.Why = fmt.Sprintf("The WiFi link dropped (%q). If it happens with good signal on every access point, suspect the PC's WiFi adapter/driver; if only on one access point, suspect that one.", in.Reason)
	}
}

// ispHop picks the ISP edge from traceroute hops (nil = hop that did not answer): the first public address that is
// neither our gateway nor the destination. Carrier-grade NAT (100.64/10) counts as public: it is already the ISP.
func ispHop(hops []net.IP, gw, dst net.IP) net.IP {
	for _, h := range hops {
		if h == nil || h.Equal(gw) {
			continue
		}
		if h.Equal(dst) {
			return nil
		}
		if !h.IsPrivate() && !h.IsLoopback() && !h.IsLinkLocalUnicast() && !h.IsUnspecified() {
			return h
		}
	}
	return nil
}
