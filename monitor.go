package main

// Glue between live state and the pure Detector: builds one Tick per second.

import (
	"fmt"
	"log"
	"math"
	"slices"
)

// buildTick evaluates thresholds on the current state. Caller must not hold st.mu.
func (a *App) buildTick() Tick {
	t := now()
	s := a.st
	s.mu.Lock()
	defer s.mu.Unlock()
	c := a.cfg // thresholds can change from the dashboard: copy them under the lock
	tk := Tick{TS: t, Viol: map[string]float64{}}
	viol := func(k string, v float64) { tk.Viol[k] = v }

	// WiFi off while on Ethernet is fine; it only counts when WiFi is (or was) the active path.
	if s.Wifi.Present && !s.Wifi.Connected && (s.Gateway == "" || s.ActiveIsWifi) {
		viol("wifi_state", 0)
		tk.Disconnected, tk.Flags.WifiDown = true, true
	}
	if s.Gateway == "" {
		viol("no_gateway", 0)
		tk.Disconnected = true
	} else if s.gwTimeouts >= c.OpenAfterSec {
		viol("loss_gateway", 100)
		tk.Disconnected, tk.Flags.GwBad = true, true
	}

	// During a speed test our own download inflates latency: only disconnections count.
	speedtest := a.speedBusy.Load() || t-a.speedEnd.Load() < 3000
	if speedtest {
		return tk
	}

	// label "" = evaluate only (no violation): used for the ISP hop, whose routers often answer ping
	// with low priority, so it may only refine a diagnosis, never open an incident.
	hop := func(name, label string, rttMax float64) bool {
		bad := false
		check := func(prefix, vk string, max float64) {
			if v, ok := s.fresh(prefix+name, t, 2000); ok && v > max {
				if label != "" {
					viol(vk+label, v)
				}
				bad = true
			}
		}
		check("rtt/", "rtt_", rttMax)
		check("loss/", "loss_", c.LossPct)
		check("jitter/", "jitter_", c.JitterMs)
		return bad
	}
	if s.IspHop != "" {
		tk.Flags.IspKnown = true
		tk.Flags.IspBad = hop("isp", "", c.RTTInternetMs)
	}
	tk.Flags.Wired = s.Gateway != "" && !s.ActiveIsWifi
	if len(s.cpuWin) == 5 {
		tk.Flags.PcBusy = slices.Min(s.cpuWin) >= c.PCCPUPct // sustained for 10 s, not a spike
	}
	if s.Gateway != "" && hop("gateway", "gateway", c.RTTGatewayMs) {
		tk.Flags.GwBad = true
	}
	tk.Flags.Inet1Bad = hop(c.InternetTargets[0], c.InternetTargets[0], c.RTTInternetMs)
	tk.Flags.Inet2Bad = hop(c.InternetTargets[1], c.InternetTargets[1], c.RTTInternetMs)

	if v, ok := s.fresh2("dns/system", t, 15000); ok && v > c.DNSMs {
		viol("dns_system", v)
		tk.Flags.DnsBad = true
	}
	if v, ok := s.fresh2("dns/"+c.InternetTargets[0], t, 15000); ok && v > c.DNSMs {
		viol("dns_"+c.InternetTargets[0], v)
		tk.Flags.DnsBad = true
	}
	if v, ok := s.fresh2("tcp/"+c.TCPTarget, t, 15000); ok && v > c.TCPMs {
		viol("tcp_1.1.1.1", v)
		tk.Flags.TcpBad = true
	}
	if s.Wifi.Connected {
		if m := a.linkMedian.Load().(float64); !math.IsNaN(m) && s.Wifi.RxMbps < m*c.LinkRatePct/100 {
			viol("link_rate", s.Wifi.RxMbps)
			tk.Flags.LinkDrop = true
		}
		// RSSI never opens an incident: it only corroborates a degraded gateway hop.
		if len(s.rssiWin) >= 30 && s.Wifi.RSSI <= median(s.rssiWin)-c.RSSIDropDB {
			tk.Flags.RssiDrop = true
		}
	}
	tk.Flags.Roamed = s.roamAt > 0 && t-s.roamAt < 15000
	return tk
}

func (a *App) detectTick() {
	tk := a.buildTick()
	// After sleep/hibernate the WiFi needs a while to reconnect: that is not a network fault.
	if last := a.lastTickAt.Swap(tk.TS); last > 0 && tk.TS-last > 15000 {
		a.event("resume", fmt.Sprintf("no samples for %ds (sleep?)", (tk.TS-last)/1000))
		a.quietUntil = tk.TS + 30000
	}
	if tk.TS < a.quietUntil {
		tk = Tick{TS: tk.TS, Viol: map[string]float64{}}
	}
	a.detMu.Lock()
	a.lastTick = tk
	opened, closed := a.det.Step(tk)
	if opened != nil {
		a.st.mu.Lock()
		opened.SSID, opened.BSSID = a.st.Wifi.SSID, a.st.Wifi.BSSID
		opened.Snapshot = toJSON(map[string]any{"values": a.st.vals, "wifi": a.st.Wifi, "gateway": a.st.Gateway})
		a.st.mu.Unlock()
		if tk.TS-a.lastReason.at < 60000 {
			opened.ReasonCode, opened.Reason = a.lastReason.code, a.lastReason.reason
		}
		a.store.InsertIncident(opened)
	}
	if closed != nil {
		a.store.UpdateIncident(closed)
		log.Printf("incident closed: %s %s (%s) %ds", closed.Type, closed.Diagnosis, closed.Trigger, (closed.End-closed.Start)/1000)
	}
	a.detMu.Unlock()
	if opened != nil {
		log.Printf("incident opened: %s %s (%s)", opened.Type, opened.Diagnosis, opened.Trigger)
		a.notify(opened)
	}
	if (opened != nil || closed != nil) && a.onChange != nil {
		a.onChange()
	}
}

var typeLabel = map[string]string{"disconnection": "Disconnection", "slowdown": "Slowdown"}

func (a *App) notify(in *Incident) {
	if !a.cfg.Notify {
		return
	}
	in.describe()
	toast(typeLabel[in.Type]+": "+in.Label, "Where: "+in.Where+". Detected by "+in.Trigger+fmt.Sprintf(" (%.0f)", in.Peak))
}

// Status for tray/header: ok, slow, down, paused.
func (a *App) status() string {
	if a.paused.Load() {
		return "paused"
	}
	a.detMu.Lock()
	defer a.detMu.Unlock()
	switch {
	case a.det.Cur == nil:
		return "ok"
	case a.det.Cur.Type == "disconnection":
		return "down"
	}
	return "slow"
}
