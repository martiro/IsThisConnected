package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	RTTGatewayMs    float64  `json:"rtt_gateway_ms"`
	RTTInternetMs   float64  `json:"rtt_internet_ms"`
	LossPct         float64  `json:"loss_pct"`
	JitterMs        float64  `json:"jitter_ms"`
	DNSMs           float64  `json:"dns_ms"`
	TCPMs           float64  `json:"tcp_ms"`
	RSSIDropDB      float64  `json:"rssi_drop_db"`
	LinkRatePct     float64  `json:"link_rate_pct"`
	DownloadPct     float64  `json:"download_pct"`
	PCCPUPct        float64  `json:"pc_cpu_pct"`
	OpenAfterSec    int      `json:"open_after_sec"`
	CloseAfterSec   int      `json:"close_after_sec"`
	SpeedtestMin    int      `json:"speedtest_interval_min"`
	SpeedtestURL    string   `json:"speedtest_url"`
	DNSTestDomain   string   `json:"dns_test_domain"`
	Notify          bool     `json:"notify"`
	InternetTargets []string `json:"internet_targets"`
	TCPTarget       string   `json:"tcp_target"`
	ScanSec         int      `json:"scan_interval_sec"` // nearby AP scan; 0 = no forced scans, record what Windows hears by itself
}

func defaultConfig() Config {
	return Config{
		RTTGatewayMs: 50, RTTInternetMs: 150, LossPct: 5, JitterMs: 30, DNSMs: 300, TCPMs: 300,
		RSSIDropDB: 10, LinkRatePct: 50, DownloadPct: 40, PCCPUPct: 90, OpenAfterSec: 5, CloseAfterSec: 15,
		SpeedtestMin: 15, SpeedtestURL: "https://speed.cloudflare.com/__down?bytes=10000000",
		DNSTestDomain: "example.com", Notify: true,
		InternetTargets: []string{"8.8.8.8", "1.1.1.1"}, TCPTarget: "1.1.1.1:443", ScanSec: 60,
	}
}

// loadConfig reads config.json, filling missing fields with defaults, and writes it back so all keys are visible.
func loadConfig(dir string) Config {
	c := defaultConfig()
	p := filepath.Join(dir, "config.json")
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			log.Printf("invalid config.json, using defaults: %v", err)
			c = defaultConfig()
		}
	}
	if len(c.InternetTargets) != 2 {
		c.InternetTargets = defaultConfig().InternetTargets
	}
	saveConfig(dir, c)
	return c
}

func saveConfig(dir string, c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644)
}

// validThresholds rejects values that would break detection (they come from the dashboard).
func validThresholds(c Config) error {
	for name, v := range map[string]float64{"rtt_gateway_ms": c.RTTGatewayMs, "rtt_internet_ms": c.RTTInternetMs,
		"jitter_ms": c.JitterMs, "dns_ms": c.DNSMs, "tcp_ms": c.TCPMs, "rssi_drop_db": c.RSSIDropDB} {
		if !(v > 0 && v <= 100000) {
			return fmt.Errorf("%s must be between 0 and 100000", name)
		}
	}
	for name, v := range map[string]float64{"loss_pct": c.LossPct, "link_rate_pct": c.LinkRatePct,
		"download_pct": c.DownloadPct, "pc_cpu_pct": c.PCCPUPct} {
		if !(v >= 0 && v <= 100) {
			return fmt.Errorf("%s must be between 0 and 100", name)
		}
	}
	if c.OpenAfterSec < 1 || c.OpenAfterSec > 600 || c.CloseAfterSec < 1 || c.CloseAfterSec > 600 {
		return fmt.Errorf("open_after_sec and close_after_sec must be between 1 and 600")
	}
	return nil
}

// setThresholds applies new thresholds live (under st.mu, where the detector reads them) and saves config.json.
// Only threshold fields are written: the other config fields are read without locks.
func (a *App) setThresholds(n Config) error {
	if err := validThresholds(n); err != nil {
		return err
	}
	a.st.mu.Lock()
	c := &a.cfg
	c.RTTGatewayMs, c.RTTInternetMs, c.LossPct, c.JitterMs = n.RTTGatewayMs, n.RTTInternetMs, n.LossPct, n.JitterMs
	c.DNSMs, c.TCPMs, c.RSSIDropDB, c.LinkRatePct = n.DNSMs, n.TCPMs, n.RSSIDropDB, n.LinkRatePct
	c.DownloadPct, c.PCCPUPct, c.OpenAfterSec, c.CloseAfterSec = n.DownloadPct, n.PCCPUPct, n.OpenAfterSec, n.CloseAfterSec
	saved := a.cfg
	a.st.mu.Unlock()
	a.detMu.Lock()
	a.det.OpenAfter, a.det.CloseAfter = n.OpenAfterSec, n.CloseAfterSec
	a.detMu.Unlock()
	a.event("config", toJSON(n))
	return saveConfig(dataDir(), saved)
}

// config returns a copy of the current configuration.
func (a *App) config() Config {
	a.st.mu.Lock()
	defer a.st.mu.Unlock()
	return a.cfg
}

// State holds the latest value of every metric; read by detector, tray, SSE.
type State struct {
	mu           sync.Mutex
	vals         map[string]float64
	prev         map[string]float64
	at           map[string]int64
	Wifi         WifiInfo
	Gateway      string
	IspHop       string // first public hop toward the Internet (ISP edge), "" = unknown
	IfIndex      uint32
	ActiveIsWifi bool // the default route goes through the WiFi adapter
	Metered      bool
	LastDL       float64
	LastDLAt     int64
	gwTimeouts   int
	roamAt       int64
	rssiWin      []float64
	Scan         map[string]ScanAP // BSSID -> last time heard in a scan
	ScanErr      string
	cpuWin       []float64 // last 10 s of CPU %
}

func (s *State) set(key string, v float64, ts int64) {
	s.mu.Lock()
	if old, ok := s.vals[key]; ok {
		s.prev[key] = old
	}
	s.vals[key], s.at[key] = v, ts
	s.mu.Unlock()
}

// fresh2 is like fresh but returns min(last, previous): slow probes (every 10 s) need
// two consecutive bad samples, otherwise one slow reply would stay "bad" for 15 ticks and open an incident.
func (s *State) fresh2(key string, now int64, maxAge int64) (float64, bool) {
	v, ok := s.fresh(key, now, maxAge)
	p, hasPrev := s.prev[key]
	if !ok || !hasPrev {
		return 0, false
	}
	return math.Min(v, p), true
}

// fresh returns the value if updated within maxAge.
func (s *State) fresh(key string, now int64, maxAge int64) (float64, bool) {
	v, ok := s.vals[key]
	return v, ok && now-s.at[key] <= maxAge
}

type App struct {
	cfg        Config
	store      *Store
	st         *State
	det        *Detector
	detMu      sync.Mutex
	paused     atomic.Bool
	speedBusy  atomic.Bool
	speedEnd   atomic.Int64
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	lastReason struct {
		at     int64
		code   int
		reason string
	}
	linkMedian atomic.Value // float64
	onChange   func()       // tray refresh
	routeSeen  bool         // pollRoute ran once (no "gateway changed" event at startup)
	traceBusy  atomic.Bool
	lastTickAt atomic.Int64
	quietUntil int64 // after resume from sleep: no detection (detectTick goroutine only)
	lastTick   Tick  // guarded by detMu, shown live as the hop chain
	icmpWarn   sync.Once
}

func newApp(cfg Config, store *Store) *App {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{cfg: cfg, store: store, ctx: ctx, cancel: cancel,
		st:  &State{vals: map[string]float64{}, prev: map[string]float64{}, at: map[string]int64{}},
		det: &Detector{OpenAfter: cfg.OpenAfterSec, CloseAfter: cfg.CloseAfterSec}}
	a.linkMedian.Store(math.NaN())
	return a
}

func now() int64 { return time.Now().UnixMilli() }

// emit updates the live state and, if persist, the database.
func (a *App) emit(key string, v float64, persist bool) {
	t := now()
	a.st.set(key, v, t)
	if persist {
		a.store.Add(key, t, v)
	}
}

func (a *App) event(kind, detail string) {
	log.Printf("event %s: %s", kind, detail)
	a.store.AddEvent(now(), kind, detail)
}

// every runs f immediately and then at each interval until shutdown, skipping while paused.
func (a *App) every(d time.Duration, f func()) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			if !a.paused.Load() {
				f()
			}
			select {
			case <-a.ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (a *App) start() {
	a.store.CloseDangling()
	a.st.LastDLAt, a.st.LastDL = a.store.Last("dl/speedtest")
	a.every(5*time.Second, a.pollRoute)
	a.pinger("gateway", func() net.IP {
		a.st.mu.Lock()
		defer a.st.mu.Unlock()
		return net.ParseIP(a.st.Gateway)
	})
	a.pinger("isp", func() net.IP {
		a.st.mu.Lock()
		defer a.st.mu.Unlock()
		return net.ParseIP(a.st.IspHop)
	})
	a.every(10*time.Minute, a.findISPHop)
	a.every(2*time.Second, a.sampleCPU())
	for _, t := range a.cfg.InternetTargets {
		ip := net.ParseIP(t)
		a.pinger(t, func() net.IP { return ip })
	}
	a.startThroughput()
	a.startWifi()
	a.every(10*time.Second, a.probeDNS)
	a.every(10*time.Second, a.probeTCP)
	a.every(5*time.Minute, a.checkMetered)
	a.every(10*time.Minute, a.refreshBaselines)
	a.every(time.Second, a.detectTick)
	a.every(5*time.Second, func() {
		if err := a.store.Flush(false); err != nil {
			log.Printf("flush: %v", err)
		}
	})
	a.every(24*time.Hour, func() { a.store.Cleanup(time.Now()) })
	a.wg.Add(1)
	go func() { // periodic speed test, first after 2 minutes
		defer a.wg.Done()
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
		a.every(time.Duration(a.cfg.SpeedtestMin)*time.Minute, func() { a.speedtest(false) })
	}()
}

func (a *App) stop() {
	a.cancel()
	a.wg.Wait()
	a.detMu.Lock()
	if in := a.det.Cur; in != nil {
		in.End = now()
		a.store.UpdateIncident(in)
	}
	a.detMu.Unlock()
	a.store.Flush(true)
	a.store.db.Close()
}

// ---------- collectors ----------

func (a *App) pollRoute() {
	gw, idx, err := bestRoute(net.ParseIP(a.cfg.InternetTargets[0]))
	g := ""
	if err == nil && !gw.Equal(net.IPv4zero) {
		g = gw.String()
	}
	a.st.mu.Lock()
	old := a.st.Gateway
	a.st.Gateway, a.st.IfIndex = g, idx
	a.st.mu.Unlock()
	if g != old {
		if a.routeSeen {
			a.event("gateway", fmt.Sprintf("%s → %s", nz(old), nz(g)))
		}
		go a.checkMetered()
		go a.findISPHop()
	}
	a.routeSeen = true
}

func nz(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// findISPHop runs a quiet traceroute toward the first Internet target and remembers the first public hop:
// pinging it tells "my router/line" apart from "the ISP" and from "the Internet beyond the ISP".
func (a *App) findISPHop() {
	a.st.mu.Lock()
	gw := net.ParseIP(a.st.Gateway)
	a.st.mu.Unlock()
	if gw == nil || !a.traceBusy.CompareAndSwap(false, true) {
		return
	}
	defer a.traceBusy.Store(false)
	c, err := newICMP()
	if err != nil {
		return
	}
	defer c.close()
	dst := net.ParseIP(a.cfg.InternetTargets[0])
	var hops []net.IP
	for ttl := byte(1); ttl <= 15 && a.ctx.Err() == nil; ttl++ {
		from, _, reached := c.echo(dst, ttl, 1000)
		hops = append(hops, from)
		if reached || ispHop(hops, gw, dst) != nil {
			break
		}
	}
	h := ""
	if ip := ispHop(hops, gw, dst); ip != nil {
		h = ip.String()
	}
	a.st.mu.Lock()
	old := a.st.IspHop
	a.st.IspHop = h
	a.st.mu.Unlock()
	if h != old {
		a.event("isp_hop", nz(old)+" → "+nz(h))
	}
}

// sampleCPU returns a collector of total CPU usage (all cores) between consecutive calls.
func (a *App) sampleCPU() func() {
	var lastIdle, lastTotal uint64
	return func() {
		idle, total, ok := cpuTimes()
		if !ok {
			return
		}
		if lastTotal > 0 && total > lastTotal {
			pct := 100 - float64(idle-lastIdle)*100/float64(total-lastTotal)
			a.emit("pc/cpu", pct, true)
			a.st.mu.Lock()
			if a.st.cpuWin = append(a.st.cpuWin, pct); len(a.st.cpuWin) > 5 {
				a.st.cpuWin = a.st.cpuWin[1:]
			}
			a.st.mu.Unlock()
		}
		lastIdle, lastTotal = idle, total
	}
}

func (a *App) pinger(name string, target func() net.IP) {
	c, err := newICMP()
	if err != nil {
		a.icmpWarn.Do(func() { // without ping nothing works: say so once, with the fix
			a.event("icmp_unavailable", err.Error())
			toast(appName+": ping not permitted", err.Error())
		})
		return
	}
	var win []float64
	n := 0
	a.every(time.Second, func() {
		ip := target()
		if ip == nil {
			return
		}
		rtt, ok := c.ping(ip, 900)
		if ok {
			a.emit("rtt/"+name, rtt, true)
		} else {
			rtt = math.NaN()
		}
		if name == "gateway" {
			a.st.mu.Lock()
			if ok {
				a.st.gwTimeouts = 0
			} else {
				a.st.gwTimeouts++
			}
			ap := ""
			if ok && a.st.ActiveIsWifi && a.st.Wifi.Connected { // per access point history, for the AP comparison
				ap = "ap/" + a.st.Wifi.BSSID + "/" + a.st.Wifi.SSID
			}
			a.st.mu.Unlock()
			if ap != "" {
				a.store.Add(ap, now(), rtt)
			}
		}
		if win = append(win, rtt); len(win) > 30 {
			win = win[1:]
		}
		loss, jit := lossJitter(win)
		n++
		persist := n%5 == 0
		a.emit("loss/"+name, loss, persist)
		a.emit("jitter/"+name, jit, persist)
	}) // the ICMP handle lives as long as the process
}

func (a *App) startThroughput() {
	var lastIn, lastOut uint64
	var lastIdx uint32
	var lastT time.Time
	a.every(time.Second, func() {
		a.st.mu.Lock()
		idx := a.st.IfIndex
		a.st.mu.Unlock()
		if idx == 0 {
			return
		}
		in, out, typ, err := ifEntry(idx)
		t := time.Now()
		if err == nil {
			a.st.mu.Lock()
			a.st.ActiveIsWifi = typ == ifTypeIEEE80211
			a.st.mu.Unlock()
		}
		if err == nil && idx == lastIdx && in >= lastIn && out >= lastOut {
			dt := t.Sub(lastT).Seconds()
			a.emit("thr/rx", float64(in-lastIn)*8/dt/1e6, true)
			a.emit("thr/tx", float64(out-lastOut)*8/dt/1e6, true)
		}
		lastIn, lastOut, lastIdx, lastT = in, out, idx, t
	})
}

func (a *App) startWifi() {
	var w *wlan
	var prev WifiInfo
	tries, tick := 0, 0
	scanTicks := max(a.cfg.ScanSec, 10) / 2 // 2 s ticks between scans
	if a.cfg.ScanSec == 0 {
		scanTicks = 30
	}
	a.every(2*time.Second, func() {
		if w == nil {
			if tries++; tries%30 != 1 { // retry opening every minute (e.g. WLAN service started later)
				return
			}
			var err error
			if w, err = openWlan(); err != nil {
				w = nil
				return
			}
		}
		wi, err := w.info()
		if err != nil {
			wi = WifiInfo{}
		}
		t := now()
		a.st.mu.Lock()
		a.st.Wifi = wi
		if wi.Connected {
			a.st.rssiWin = append(a.st.rssiWin, wi.RSSI)
			if len(a.st.rssiWin) > 300 { // 10 minutes at 2 s
				a.st.rssiWin = a.st.rssiWin[1:]
			}
		}
		if prev.Connected && wi.Connected && prev.SSID == wi.SSID && prev.BSSID != wi.BSSID {
			a.st.roamAt = t
		}
		a.st.mu.Unlock()
		if wi.Connected {
			a.emit("wifi/rssi", wi.RSSI, true)
			a.emit("wifi/quality", wi.Quality, true)
			a.emit("wifi/rx_rate", wi.RxMbps, true)
			a.emit("wifi/tx_rate", wi.TxMbps, true)
		}
		switch {
		case prev.Connected && !wi.Connected:
			a.event("wifi_disconnected", prev.SSID)
			go a.fetchReason()
		case !prev.Connected && wi.Connected:
			a.event("wifi_connected", fmt.Sprintf("%s (%s, channel %d, %s)", wi.SSID, wi.BSSID, wi.Channel, wi.Band))
		case wi.Connected && prev.SSID != wi.SSID:
			a.event("ssid", prev.SSID+" → "+wi.SSID)
		case wi.Connected && prev.BSSID != wi.BSSID:
			a.event("roaming", prev.BSSID+" → "+wi.BSSID)
		case wi.Connected && prev.Channel != wi.Channel:
			a.event("channel", fmt.Sprintf("%d → %d (%s)", prev.Channel, wi.Channel, wi.Band))
		}
		prev = wi
		switch tick++; tick % scanTicks { // same goroutine as info(): the wlan handle is not shared
		case 1:
			if a.cfg.ScanSec > 0 {
				w.triggerScan()
			}
		case 4: // 6 s later: a full scan takes 2-4 s
			aps, err := w.scan()
			a.recordScan(aps, err)
		}
	})
}

type ScanAP struct {
	BSS
	Seen int64 `json:"seen"`
}

// recordScan keeps the APs for the spectrum view and stores each one's RSSI as scan/<band>/<bssid>/<ssid>.
func (a *App) recordScan(aps []BSS, err error) {
	t := now()
	a.st.mu.Lock()
	a.st.ScanErr = ""
	if err != nil {
		a.st.ScanErr = err.Error()
	}
	if a.st.Scan == nil {
		a.st.Scan = map[string]ScanAP{}
	}
	for k, x := range a.st.Scan {
		if t-x.Seen > 10*60000 {
			delete(a.st.Scan, k)
		}
	}
	for _, b := range aps {
		a.st.Scan[b.BSSID] = ScanAP{b, t}
	}
	a.st.mu.Unlock()
	for _, b := range aps {
		a.store.Add("scan/"+strings.TrimSuffix(b.Band, " GHz")+"/"+b.BSSID+"/"+b.SSID, t, b.RSSI)
	}
}

// fetchReason waits for WLAN AutoConfig to write the event, then attaches the reason to the incident.
func (a *App) fetchReason() {
	time.Sleep(3 * time.Second)
	code, reason, ok := lastDisconnectReason(2 * time.Minute)
	if !ok {
		return
	}
	a.event("disconnect_reason", fmt.Sprintf("%d: %s", code, reason))
	a.detMu.Lock()
	defer a.detMu.Unlock()
	a.lastReason.at, a.lastReason.code, a.lastReason.reason = now(), code, reason
	if in := a.det.Cur; in != nil && in.Reason == "" {
		in.ReasonCode, in.Reason = code, reason
		a.store.UpdateIncident(in)
	}
}

func (a *App) probeDNS() {
	label := fmt.Sprintf("wm%x", rand.Uint32())     // random label defeats caches: measures a full resolution
	name := label + "." + a.cfg.DNSTestDomain + "." // trailing dot: NXDOMAIN must not retry with the DNS search suffixes
	direct := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(a.cfg.InternetTargets[0], "53"))
	}}
	for key, r := range map[string]*net.Resolver{"dns/system": net.DefaultResolver, "dns/" + a.cfg.InternetTargets[0]: direct} {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
		t := time.Now()
		_, err := r.LookupHost(ctx, name)
		cancel()
		ms := float64(time.Since(t).Microseconds()) / 1000
		if dnsErr, ok := err.(*net.DNSError); err != nil && !(ok && dnsErr.IsNotFound) {
			ms = 2000 // shortcut: failure stored as timeout value, so it shows on the chart and trips the threshold
		}
		a.emit(key, ms, true)
	}
}

func (a *App) probeTCP() {
	t := time.Now()
	c, err := net.DialTimeout("tcp", a.cfg.TCPTarget, 2*time.Second)
	ms := float64(time.Since(t).Microseconds()) / 1000
	if err != nil {
		ms = 2000
	} else {
		c.Close()
	}
	a.emit("tcp/"+a.cfg.TCPTarget, ms, true)
}

func (a *App) checkMetered() {
	m, ok := isMetered()
	if !ok {
		return
	}
	a.st.mu.Lock()
	changed := a.st.Metered != m
	a.st.Metered = m
	a.st.mu.Unlock()
	if changed {
		a.event("metered", fmt.Sprint(m))
	}
}

func (a *App) refreshBaselines() {
	if m, ok := a.store.MedianSince("wifi/rx_rate", time.Now().Add(-24*time.Hour).UnixMilli(), 60); ok {
		a.linkMedian.Store(m)
	}
}

// speedtest downloads the test file. manual=true ignores the metered/busy checks.
func (a *App) speedtest(manual bool) (float64, error) {
	if !a.speedBusy.CompareAndSwap(false, true) {
		return 0, fmt.Errorf("a test is already running")
	}
	defer a.speedBusy.Store(false)
	a.st.mu.Lock()
	metered, rx, last := a.st.Metered, a.st.vals["thr/rx"], a.st.LastDL
	a.st.mu.Unlock()
	if !manual && (metered || (last > 0 && rx > last*0.5)) {
		return 0, fmt.Errorf("skipped (metered connection or network busy)")
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.cfg.SpeedtestURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.speedEnd.Store(now())
		return 0, err
	}
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	a.speedEnd.Store(now())
	if err != nil || n == 0 {
		return 0, fmt.Errorf("download interrupted: %v", err)
	}
	mbps := float64(n) * 8 / time.Since(start).Seconds() / 1e6
	median, hasMedian := a.store.MedianSince("dl/speedtest", time.Now().Add(-30*24*time.Hour).UnixMilli(), 3)
	a.emit("dl/speedtest", mbps, true)
	a.st.mu.Lock()
	a.st.LastDL, a.st.LastDLAt = mbps, now()
	a.st.mu.Unlock()
	if hasMedian && mbps < median*a.config().DownloadPct/100 {
		a.downloadLow(start.UnixMilli(), mbps)
	}
	return mbps, nil
}

// downloadLow attaches a low speed test to the open incident, or records a standalone one.
func (a *App) downloadLow(start int64, mbps float64) {
	a.detMu.Lock()
	defer a.detMu.Unlock()
	if in := a.det.Cur; in != nil {
		in.addMetric("download")
		in.flags.DlLow = true
		in.Diagnosis = diagnose(in.flags)
		a.store.UpdateIncident(in)
		return
	}
	a.st.mu.Lock()
	in := &Incident{Start: start, End: now(), Type: "slowdown", Trigger: "download", Metrics: []string{"download"},
		Peak: mbps, Diagnosis: "low_bandwidth", SSID: a.st.Wifi.SSID, BSSID: a.st.Wifi.BSSID, Snapshot: toJSON(a.st.vals)}
	a.st.mu.Unlock()
	a.store.InsertIncident(in)
	a.notify(in)
}
