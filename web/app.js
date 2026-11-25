"use strict";

// ---------- configuration ----------
const SERIES = {
  "rtt/gateway":        { label: "Ping router",          color: "#2563eb", unit: "ms" },
  "rtt/isp":            { label: "Ping ISP (first hop)", color: "#b45309", unit: "ms" },
  "rtt/8.8.8.8":        { label: "Ping 8.8.8.8",         color: "#d9480f", unit: "ms" },
  "rtt/1.1.1.1":        { label: "Ping 1.1.1.1",         color: "#7c3aed", unit: "ms" },
  "dns/system":        { label: "DNS (system)",         color: "#0d9488", unit: "ms", gaps: true },
  "dns/8.8.8.8":        { label: "DNS 8.8.8.8",          color: "#65a30d", unit: "ms", gaps: true, hidden: true },
  "tcp/1.1.1.1:443":    { label: "TCP connect 1.1.1.1",  color: "#c026d3", unit: "ms", gaps: true },
  "loss/gateway":       { label: "Loss router",          color: "#2563eb", unit: "%", gaps: true },
  "loss/isp":           { label: "Loss ISP",             color: "#b45309", unit: "%", gaps: true },
  "loss/8.8.8.8":       { label: "Loss 8.8.8.8",         color: "#d9480f", unit: "%", gaps: true },
  "loss/1.1.1.1":       { label: "Loss 1.1.1.1",         color: "#7c3aed", unit: "%", gaps: true },
  "jitter/gateway":     { label: "Jitter router",        color: "#60a5fa", unit: "ms", gaps: true, dash: true },
  "jitter/isp":         { label: "Jitter ISP",           color: "#f59e0b", unit: "ms", gaps: true, dash: true, hidden: true },
  "jitter/8.8.8.8":     { label: "Jitter 8.8.8.8",       color: "#fb923c", unit: "ms", gaps: true, dash: true },
  "jitter/1.1.1.1":     { label: "Jitter 1.1.1.1",       color: "#a78bfa", unit: "ms", gaps: true, dash: true },
  "wifi/rssi":          { label: "Signal (RSSI)",        color: "#0891b2", unit: "dBm", gaps: true },
  "wifi/quality":       { label: "Signal quality",       color: "#64748b", unit: "%", gaps: true, hidden: true },
  "wifi/rx_rate":       { label: "Link speed rx",        color: "#16a34a", unit: "Mbit/s", gaps: true },
  "wifi/tx_rate":       { label: "Link speed tx",        color: "#ca8a04", unit: "Mbit/s", gaps: true },
  "thr/rx":             { label: "Traffic ↓",            color: "#2563eb", unit: "Mbit/s" },
  "thr/tx":             { label: "Traffic ↑",            color: "#db2777", unit: "Mbit/s" },
  "dl/speedtest":       { label: "Download test",        color: "#16a34a", unit: "Mbit/s", points: true },
  "pc/cpu":             { label: "CPU",                  color: "#475569", unit: "%", gaps: true },
};
const PANELS = [
  { title: "Latency, from your PC outwards: router → ISP → Internet → DNS", keys: ["rtt/gateway", "rtt/isp", "rtt/8.8.8.8", "rtt/1.1.1.1", "dns/system", "dns/8.8.8.8", "tcp/1.1.1.1:443"] },
  { title: "Packet loss and jitter (last 30 s)", keys: ["loss/gateway", "loss/isp", "loss/8.8.8.8", "loss/1.1.1.1", "jitter/gateway", "jitter/isp", "jitter/8.8.8.8", "jitter/1.1.1.1"] },
  { title: "WiFi signal and link speed", keys: ["wifi/rssi", "wifi/quality", "wifi/rx_rate", "wifi/tx_rate"] },
  { title: "Throughput", keys: ["thr/rx", "thr/tx", "dl/speedtest"] },
  { title: "This PC: CPU load", keys: ["pc/cpu"] },
];
// band colours on the charts, by diagnosis key (labels/explanations come from the server)
const DIAG_COLOR = {
  wifi_disconnect: "#d93b3b", pc_overload: "#475569", roaming: "#8b5cf6", wired_lan: "#0369a1",
  weak_signal: "#f97316", ap_congestion: "#eab308", isp_wan: "#be123c", internet_upstream: "#9f1239",
  dns: "#0d9488", link_degraded: "#ca8a04", tcp_slow: "#c026d3", low_bandwidth: "#2563eb", single_host: "#64748b", other: "#94a3b8",
};
const TYPE = { disconnection: "Disconnection", slowdown: "Slowdown" };
const TRIGGER = {
  wifi_state: "WiFi state", no_gateway: "No router (gateway)", rtt_gateway: "Ping router", loss_gateway: "Loss router",
  jitter_gateway: "Jitter router", "rtt_8.8.8.8": "Ping 8.8.8.8", "loss_8.8.8.8": "Loss 8.8.8.8", "jitter_8.8.8.8": "Jitter 8.8.8.8",
  "rtt_1.1.1.1": "Ping 1.1.1.1", "loss_1.1.1.1": "Loss 1.1.1.1", "jitter_1.1.1.1": "Jitter 1.1.1.1", dns_system: "DNS (system)",
  "dns_8.8.8.8": "DNS 8.8.8.8", "tcp_1.1.1.1": "TCP connect 1.1.1.1", link_rate: "WiFi link speed", download: "Download test",
};
const EVENT_LINES = new Set(["roaming", "ssid", "channel", "wifi_disconnected", "wifi_connected", "gateway", "isp_hop", "resume"]);

const $ = (s) => document.querySelector(s);
const S = { live: true, span: 3600e3, from: 0, to: 0, overlay: false, data: null, incidents: [], events: [], sessions: [], bssid: "", cfg: null, viol: {}, linkMedian: 0,
  sort: { k: "start", dir: -1 }, sel: null };
let charts = [];

// ---------- formatting ----------
const fmtTime = (ms) => new Date(ms).toLocaleString("en-GB", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
const fmtDur = (ms) => { const s = Math.round(ms / 1000); return s < 60 ? `${s} s` : s < 3600 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`; };
const unitOf = (trig) => trig.startsWith("loss") ? "%" : (trig === "link_rate" || trig === "download") ? "Mbit/s" : (trig === "wifi_state" || trig === "no_gateway") ? "" : "ms";
const num = (v, d = 0) => v == null || Number.isNaN(v) ? "—" : v.toFixed(d);
const css = (v) => getComputedStyle(document.documentElement).getPropertyValue(v).trim();
const toLocalInput = (ms) => { const d = new Date(ms - new Date(ms).getTimezoneOffset() * 60000); return d.toISOString().slice(0, 16); };
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

// ---------- range ----------
function currentRange() {
  if (S.live) { S.to = Date.now(); S.from = S.to - S.span; }
  return [Math.round(S.from), Math.round(S.to)];
}
function setRange(from, to) {
  S.live = false; S.from = from; S.to = Math.min(to, Date.now()); S.span = S.to - S.from;
  syncControls(); load();
}
function goLive(span) {
  S.live = true; if (span) S.span = span;
  syncControls(); load();
}
function syncControls() {
  document.querySelectorAll("#presets button").forEach((b) => b.setAttribute("aria-pressed", String(S.live && +b.dataset.span === S.span)));
  $("#btnLive").setAttribute("aria-pressed", String(S.live));
  const [f, t] = currentRange();
  $("#from").value = toLocalInput(f); $("#to").value = toLocalInput(t);
  $("#btnCsv").href = `/api/export.csv?from=${f}&to=${t}`;
}

// ---------- data ----------
let loading = false;
async function load() {
  if (loading) return;
  loading = true;
  try {
    const [f, t] = currentRange();
    const q = `from=${f}&to=${t}`;
    const get = (u) => fetch(`${u}?${q}`).then((r) => r.json());
    const [series, inc, ev, aps] = await Promise.all([get("/api/series"), get("/api/incidents"), get("/api/events"), get("/api/aps")]);
    S.data = series; S.incidents = inc; S.events = ev; S.sessions = aps;
    $("#rangeInfo").textContent = `${fmtTime(f)} → ${fmtTime(t)} · ${series.raw ? "per-second data" : "per-minute averages"}${series.step > 1000 ? ` (one point every ${fmtDur(series.step)})` : ""}`;
    drawCharts(); renderIncidents(); renderSessions(); updateCfgInfo();
  } catch (e) {
    $("#rangeInfo").textContent = "Error loading data: " + e.message;
  } finally { loading = false; }
}

// ---------- charts ----------
function bandsPlugin() {
  return {
    hooks: {
      drawClear: (u) => {
        const ctx = u.ctx, { top, height } = u.bbox, now = Date.now();
        ctx.save();
        for (const inc of S.incidents) {
          const x0 = u.valToPos(inc.start / 1000, "x", true), x1 = u.valToPos((inc.end || now) / 1000, "x", true);
          const color = DIAG_COLOR[inc.diagnosis] || DIAG_COLOR.other;
          ctx.fillStyle = color + (S.sel === inc.id ? "55" : "26");
          ctx.fillRect(x0, top, Math.max(x1 - x0, 3 * devicePixelRatio), height);
        }
        ctx.setLineDash([4 * devicePixelRatio, 4 * devicePixelRatio]);
        ctx.strokeStyle = css("--muted"); ctx.lineWidth = devicePixelRatio;
        for (const e of S.events) {
          if (!EVENT_LINES.has(e.kind)) continue;
          const x = u.valToPos(e.ts / 1000, "x", true);
          ctx.beginPath(); ctx.moveTo(x, top); ctx.lineTo(x, top + height); ctx.stroke();
        }
        ctx.restore();
      },
    },
  };
}

function scaleOf(key) {
  const u = SERIES[key].unit;
  return u === "Mbit/s" ? "mbps" : u === "%" ? "pct" : u === "dBm" ? "dbm" : "ms";
}

function makeOpts(keys, width) {
  const axisColor = css("--muted"), grid = css("--grid");
  // visible series decide the axes; hidden-by-default ones (e.g. quality %) are read from the legend
  const scales = S.overlay ? ["ms", "mbps"] : [...new Set([...keys.filter((k) => !SERIES[k].hidden), ...keys].map(scaleOf))].slice(0, 2);
  const axisLabel = { ms: "ms", mbps: "Mbit/s", pct: "%", dbm: "dBm" };
  const axes = [{ stroke: axisColor, grid: { stroke: grid }, ticks: { stroke: grid } }];
  scales.forEach((sc, i) => axes.push({
    scale: sc, side: i === 0 ? 3 : 1, label: axisLabel[sc], stroke: axisColor,
    grid: { show: i === 0, stroke: grid }, ticks: { stroke: grid }, size: 60,
  }));
  if (scales.length === 1) // empty right axis keeps every panel's time axis aligned
    axes.push({ scale: scales[0], side: 1, size: 60, label: " ", values: () => [], grid: { show: false }, ticks: { show: false } });
  return {
    width, height: S.overlay ? 420 : keys.length === 1 ? 150 : 210,
    cursor: { sync: { key: "wm" }, drag: { x: true, y: false, setScale: false } },
    select: { show: true },
    legend: { live: true },
    scales: { x: { time: true }, pct: { range: [0, 100] } },
    axes,
    series: [{ value: (u, v) => v == null ? "—" : fmtTime(v * 1000) }, ...keys.map((k) => {
      const c = SERIES[k];
      const s = {
        label: c.label, stroke: c.color, scale: scaleOf(k), width: 1.5, spanGaps: !!c.gaps, show: !c.hidden,
        points: c.points ? { show: true, size: 7, fill: c.color } : { show: false },
        value: (u, v) => v == null ? "—" : `${v.toFixed(c.unit === "Mbit/s" ? 1 : 0)} ${c.unit}`,
      };
      if (c.dash) s.dash = [5, 4];
      if (c.points) s.paths = () => null; // speed tests are sparse: dots only
      return s;
    })],
    plugins: [bandsPlugin()],
    hooks: {
      setSelect: [(u) => {
        if (u.select.width < 5) return;
        const a = u.posToVal(u.select.left, "x") * 1000, b = u.posToVal(u.select.left + u.select.width, "x") * 1000;
        u.setSelect({ width: 0, height: 0 }, false);
        setRange(a, b);
      }],
    },
  };
}

function drawCharts() {
  const root = $("#charts");
  const panels = S.overlay ? [{ title: "All metrics", keys: Object.keys(SERIES) }] : PANELS;
  const [f, t] = currentRange();
  const d = S.data;
  const hidden = new Map(); // keep the user's legend toggles across reloads
  charts.forEach((c) => c.series.forEach((s, i) => i && hidden.set(s.label, s.show)));
  const rebuild = charts.length !== panels.length || root.dataset.mode !== String(S.overlay);
  if (rebuild) {
    charts.forEach((c) => c.destroy()); charts = []; root.innerHTML = ""; root.dataset.mode = String(S.overlay);
  }
  panels.forEach((p, i) => {
    const data = [d.x, ...p.keys.map((k) => d.series[k] || new Array(d.x.length).fill(null))];
    if (!rebuild) { charts[i].setData(data, false); charts[i].setScale("x", { min: f / 1000, max: t / 1000 }); return; }
    const el = document.createElement("div");
    el.className = "panel";
    el.innerHTML = `<h3>${p.title}</h3>`;
    root.appendChild(el);
    const opts = makeOpts(p.keys, el.clientWidth - 20);
    opts.series.forEach((s, j) => { if (j && hidden.has(s.label)) s.show = hidden.get(s.label); });
    opts.scales.x.range = () => [currentRange()[0] / 1000, currentRange()[1] / 1000];
    const u = new uPlot(opts, data, el);
    charts.push(u);
    el.addEventListener("wheel", onWheel, { passive: false });
  });
}

function onWheel(e) {
  if (!e.shiftKey && !e.ctrlKey) return;
  e.preventDefault();
  const [f, t] = currentRange(), span = t - f;
  if (e.ctrlKey) {
    const k = e.deltaY > 0 ? 1.5 : 1 / 1.5, mid = (f + t) / 2;
    setRange(mid - span * k / 2, mid + span * k / 2);
  } else {
    const shift = span * 0.15 * Math.sign(e.deltaY || e.deltaX);
    setRange(f + shift, t + shift);
  }
}

new ResizeObserver(() => charts.forEach((u) => u.setSize({ width: u.root.parentElement.clientWidth - 20, height: u.height }))).observe($("#charts"));

// ---------- incidents ----------
function renderIncidents() {
  const fType = $("#fType").value, fWhere = $("#fWhere").value, now = Date.now();
  const sel = $("#fWhere");
  const known = new Set([...sel.options].map((o) => o.value));
  [...new Set(S.incidents.map((i) => i.where))].forEach((w) => { if (!known.has(w)) sel.add(new Option(w, w)); });

  const rows = S.incidents
    .filter((i) => (!fType || i.type === fType) && (!fWhere || i.where === fWhere))
    .map((i) => ({ ...i, dur: (i.end || now) - i.start }));
  const { k, dir } = S.sort;
  rows.sort((a, b) => (a[k] > b[k] ? 1 : a[k] < b[k] ? -1 : 0) * dir);
  document.querySelectorAll("#incTable th").forEach((th) => th.setAttribute("aria-sort", th.dataset.k === k ? (dir > 0 ? "ascending" : "descending") : "none"));

  $("#incCount").textContent = `(${rows.length})`;
  $("#incEmpty").hidden = rows.length > 0;
  $("#incTable tbody").innerHTML = rows.map((i) => {
    const color = DIAG_COLOR[i.diagnosis] || DIAG_COLOR.other;
    const u = unitOf(i.trigger);
    return `<tr data-id="${i.id}" tabindex="0" class="${S.sel === i.id ? "sel" : ""}">
      <td>${fmtTime(i.start)}</td>
      <td>${i.end ? fmtDur(i.dur) : `<span class="ongoing">ongoing · ${fmtDur(i.dur)}</span>`}</td>
      <td>${esc(TYPE[i.type] || i.type)}</td>
      <td><span class="chip"><i style="background:${color}"></i><strong>${esc(i.where)}</strong></span></td>
      <td>${esc(i.label)}</td>
      <td>${esc(TRIGGER[i.trigger] || i.trigger)}</td>
      <td>${u ? `${num(i.peak, u === "Mbit/s" ? 1 : 0)} ${u}` : "—"}</td>
      <td class="reason">${esc(i.reason ? `${i.reason} (code ${i.reason_code})` : "")}</td></tr>`;
  }).join("");
}

function showIncident(id) {
  const i = S.incidents.find((x) => x.id === id);
  if (!i) return;
  S.sel = id;
  const end = i.end || Date.now();
  let snap = i.snapshot;
  try { snap = JSON.stringify(JSON.parse(i.snapshot), null, 2); } catch { /* keep raw */ }
  const box = $("#detail");
  box.hidden = false;
  box.innerHTML = `<h3>${esc(i.label)} — ${fmtTime(i.start)}</h3>
    <p class="where">Where: <strong>${esc(i.where)}</strong></p>
    <p class="why">${esc(i.why)}</p>
    <p class="muted">Type: ${esc(TYPE[i.type] || i.type)} · Detected by: ${esc(TRIGGER[i.trigger] || i.trigger)} ·
    Metrics involved: ${i.metrics.map((m) => esc(TRIGGER[m] || m)).join(", ")} · Network: ${esc(i.ssid || "—")} ${esc(i.bssid || "")}</p>
    ${i.reason ? `<p class="muted">Windows reason: ${esc(i.reason)} (code ${i.reason_code})</p>` : ""}
    <details><summary>All metric values when the incident started</summary><pre>${esc(snap)}</pre></details>`;
  setRange(i.start - 5 * 60e3, end + 5 * 60e3);
}

// ---------- access points: one row per connection ----------
const AP_DIAG = new Set(["wifi_disconnect", "roaming", "weak_signal", "ap_congestion", "link_degraded"]); // blamed on WiFi/AP
const AP_COLORS = ["#2563eb", "#d9480f", "#16a34a", "#7c3aed", "#0891b2", "#db2777", "#ca8a04", "#475569"];

function apIncidents(bssid, from, to) {
  return S.incidents.filter((i) => i.bssid === bssid && AP_DIAG.has(i.diagnosis) && i.start >= from && i.start <= to);
}

// per access point totals and a verdict compared with the other access points
function apSummary() {
  const by = new Map();
  for (const x of S.sessions) {
    const a = by.get(x.bssid) || { bssid: x.bssid, ssid: x.ssid, ms: 0, pingW: 0, conns: 0, inc: 0 };
    const d = Math.max(x.end - x.start, 1000);
    a.ms += d; a.pingW += x.avg_ms * d; a.conns++; a.inc += apIncidents(x.bssid, x.start - 5e3, x.end + 30e3).length;
    by.set(x.bssid, a);
  }
  const aps = [...by.values()].sort((a, b) => b.ms - a.ms);
  aps.forEach((a, i) => {
    a.color = AP_COLORS[i % AP_COLORS.length];
    a.ping = a.pingW / a.ms; a.h = a.ms / 3600e3;
    const o = aps.filter((x) => x !== a), oh = o.reduce((s, x) => s + x.h, 0);
    const oRate = o.reduce((s, x) => s + x.inc, 0) / oh, oPing = o.reduce((s, x) => s + x.pingW, 0) / (oh * 3600e3);
    a.verdict = !o.length || a.ms < 10 * 60e3 ? "" :
      a.inc >= 2 && a.inc / a.h > 2 * oRate ? "More problems than the other access points" :
      a.ping > 2 * oPing && a.ping > 20 ? "Slower than the other access points" : "";
  });
  return new Map(aps.map((a) => [a.bssid, a]));
}

function renderSessions() {
  const now = Date.now(), aps = apSummary();
  const switches = S.sessions.filter((x) => x.next).length;
  $("#sessCount").textContent = `(${S.sessions.length} connections, ${switches} switches)`;
  $("#sessEmpty").hidden = S.sessions.length > 0;
  const tag = (b) => { const a = aps.get(b); return `<span class="ap-tag"><i style="background:${a ? a.color : "#94a3b8"}"></i><code>${esc(b)}</code></span>`; };
  $("#apSummary").innerHTML = [...aps.values()].map((a) =>
    `<span class="ap-card">${tag(a.bssid)} ${esc(a.ssid)} · ${fmtDur(a.ms)} · ${a.conns} conn. · ping ${num(a.ping)} ms ·
     ${a.inc} WiFi incidents${a.h >= 1 / 6 ? ` (${num(a.inc / a.h, 1)}/h)` : ""}${a.verdict ? ` <span class="badge bad">${a.verdict}</span>` : ""}</span>`).join("") +
    (aps.size === 1 ? `<span class="muted small">Only one access point used: nothing to compare. At home the access point is usually the router itself;
      to tell them apart, use a cable for a while: if the problems stop, blame the WiFi/access point, otherwise the router or beyond.</span>` : "");
  const rows = [...S.sessions].reverse().slice(0, 300); // newest first
  $("#sessTable tbody").innerHTML = rows.map((x, i) => {
    const current = i === 0 && x.bssid === S.bssid && now - x.end < 60e3;
    const dur = x.end - x.start, inc = apIncidents(x.bssid, x.start - 5e3, x.end + 30e3).length;
    const then = current ? '<span class="badge ok">connected now</span>'
      : x.next ? `Switched to ${tag(x.next)}` : "Connection lost (or IsThisConnected not running)";
    return `<tr data-from="${x.start}" data-to="${x.end}" tabindex="0">
      <td>${fmtTime(x.start)}</td><td>${current ? "—" : fmtTime(x.end)}</td>
      <td class="${!current && x.next && dur < 120e3 ? "warn" : ""}">${fmtDur(dur)}</td>
      <td>${esc(x.ssid)}</td><td>${tag(x.bssid)}</td>
      <td>${num(x.avg_ms)} · ${num(x.p95_ms)} ms</td>
      <td class="${inc ? "bad" : ""}">${inc}</td><td>${then}</td></tr>`;
  }).join("");
}

// ---------- settings ----------
// threshold key, label, unit, what a good connection looks like, series to compare with, which incident triggers it owns
const CFG = [
  { k: "rtt_gateway_ms", goodLimit: 10, label: "Ping to router above", unit: "ms", good: "< 10 ms on WiFi, < 2 ms on cable", series: ["rtt/gateway"], trig: (t) => t === "rtt_gateway" },
  { k: "rtt_internet_ms", goodLimit: 50, label: "Ping to Internet above", unit: "ms", good: "< 50 ms (Europe)", series: ["rtt/8.8.8.8", "rtt/1.1.1.1"], trig: (t) => t.startsWith("rtt_") && t !== "rtt_gateway" },
  { k: "loss_pct", goodLimit: 1, label: "Packet loss above", unit: "%", good: "0 % (< 1 % acceptable)", series: ["loss/gateway", "loss/8.8.8.8", "loss/1.1.1.1"], trig: (t) => t.startsWith("loss_") },
  { k: "jitter_ms", goodLimit: 10, label: "Jitter above", unit: "ms", good: "< 10 ms (calls need < 30 ms)", series: ["jitter/gateway", "jitter/8.8.8.8", "jitter/1.1.1.1"], trig: (t) => t.startsWith("jitter_") },
  { k: "dns_ms", goodLimit: 150, label: "DNS lookup above", unit: "ms", good: "< 50 ms (< 150 ms acceptable)", series: ["dns/system", "dns/8.8.8.8"], trig: (t) => t.startsWith("dns_") },
  { k: "tcp_ms", goodLimit: 50, label: "TCP connect above", unit: "ms", good: "< 50 ms", series: ["tcp/1.1.1.1:443"], trig: (t) => t.startsWith("tcp_") },
  { k: "link_rate_pct", goodLimit: 50, label: "WiFi link speed below", unit: "% of usual", good: "steady; short dips to 30–50 % are normal on busy WiFi (0 = check off)", series: ["wifi/rx_rate"], below: true, trig: (t) => t === "link_rate" },
  { k: "download_pct", label: "Download test below", unit: "% of 30-day median", good: "≥ 80 % of your usual speed", series: ["dl/speedtest"], trig: (t) => t === "download" },
  { k: "rssi_drop_db", label: "Signal drop for \"weak signal\"", unit: "dB", good: "signal better than −65 dBm; it normally moves 3–6 dB", series: ["wifi/rssi"], diag: "weak_signal" },
  { k: "pc_cpu_pct", goodLimit: 50, label: "PC overloaded when CPU above", unit: "% for 10 s", good: "average under 50 %", series: ["pc/cpu"], diag: "pc_overload" },
  { k: "open_after_sec", label: "Open an incident after", unit: "s of bad values", good: "5–10 s: shorter glitches are ignored" },
  { k: "close_after_sec", label: "Close an incident after", unit: "s of normal values", good: "15–30 s" },
];

function seriesVals(keys) {
  const out = [];
  for (const k of keys || []) for (const v of (S.data?.series[k] || [])) if (v != null) out.push(v);
  return out.sort((a, b) => a - b);
}

// threshold in the metric's own unit (link speed is a share of its 24 h median); null = not comparable with the chart
function absThreshold(c, v) {
  if (c.k === "link_rate_pct") return S.linkMedian ? S.linkMedian * v / 100 : null;
  return c.k === "rssi_drop_db" || c.k === "download_pct" ? null : v;
}

function cfgInfo(c, v) {
  const vals = seriesVals(c.series);
  let yours = c.series ? "no data in range" : "—", often = false;
  if (vals.length) {
    const q = (p) => vals[Math.min(vals.length - 1, Math.floor(p * vals.length))];
    const unit = SERIES[c.series[0]].unit, d = unit === "Mbit/s" ? 1 : 0;
    yours = c.below ? `typical ${num(q(0.5), d)} ${unit} · worst 5% under ${num(q(0.05), d)} ${unit}`
      : `typical ${num(q(0.5), d)} ${unit} · worst 5% over ${num(q(0.95), d)} ${unit}`;
    if (c.k === "rssi_drop_db") yours = `typical ${num(q(0.5))} dBm · weakest 5% ${num(q(0.05))} dBm`;
    if (c.k === "download_pct") yours = `${vals.length} tests · typical ${num(q(0.5), 1)} Mbit/s · slowest ${num(vals[0], 1)} Mbit/s`;
    const thr = absThreshold(c, v);
    if (thr != null && v > 0) {
      const over = vals.filter((x) => c.below ? x < thr : x > thr).length / vals.length * 100;
      yours += `<br>beyond threshold${c.k === "link_rate_pct" ? ` (${num(thr)} Mbit/s)` : ""}: <strong>${num(over, 1)}% of the time</strong>`;
      often = over >= 2; // beyond the threshold this often = it fires on ordinary moments, likely too strict
    }
  }
  const opened = c.trig ? S.incidents.filter((i) => c.trig(i.trigger)).length : 0;
  const involved = c.trig ? S.incidents.filter((i) => i.metrics.some(c.trig)).length : 0;
  const byDiag = c.diag ? S.incidents.filter((i) => i.diagnosis === c.diag).length : 0;
  const now = !!c.trig && Object.keys(S.viol || {}).some(c.trig);
  let inc = c.trig ? `${opened}${involved > opened ? ` <span class="muted">(involved in ${involved})</span>` : ""}`
    : c.diag ? `<span class="muted">changed the diagnosis of ${byDiag}</span>` : "—";
  if (now) inc += ' <span class="badge bad">over now</span>';
  if (often && opened) { // stricter than a good connection = false alarms; looser = the network really has the problem
    const strict = c.below ? v >= c.goodLimit : v <= c.goodLimit;
    inc += strict ? '<br><span class="warn-text">exceeded often: probably too strict (false alarms)</span>'
      : '<br><span class="warn-text">exceeded often although the threshold is lenient: a real problem worth checking</span>';
  }
  return { yours, inc, fire: opened > 0 || now };
}

function renderCfgRows(cfg) { // built once per open: live refreshes must not wipe what the user is typing
  $("#cfgRows").innerHTML = CFG.map((c) => `<tr id="cr-${c.k}">
    <th scope="row">${c.label}</th>
    <td class="nowrap"><input type="number" name="${c.k}" value="${cfg[c.k]}" min="0" step="any" required aria-label="${c.label}"> <span class="muted">${c.unit}</span></td>
    <td class="muted">${c.good}</td><td id="cv-${c.k}"></td><td id="ci-${c.k}"></td></tr>`).join("");
  updateCfgInfo();
}

function updateCfgInfo() {
  let firing = 0;
  for (const c of CFG) {
    const input = document.querySelector(`#cfgForm [name="${c.k}"]`);
    const v = input ? Number(input.value) : S.cfg?.[c.k];
    const info = cfgInfo(c, v);
    if (info.fire) firing++;
    const row = $(`#cr-${c.k}`);
    if (!row) continue;
    row.classList.toggle("fire", info.fire);
    $(`#cv-${c.k}`).innerHTML = info.yours;
    $(`#ci-${c.k}`).innerHTML = info.inc;
  }
  const b = $("#cfgBadge");
  b.hidden = !firing; b.textContent = firing;
  b.title = `${firing} thresholds opened incidents in the selected range`;
}

async function openSettings(show) {
  $("#settings").hidden = !show;
  $("#btnSettings").setAttribute("aria-expanded", String(show));
  if (!show) return;
  S.cfg = await fetch("/api/config").then((r) => r.json());
  renderCfgRows(S.cfg);
  $("#cfgMsg").textContent = "";
}
$("#btnSettings").addEventListener("click", () => openSettings($("#settings").hidden));
$("#cfgClose").addEventListener("click", () => openSettings(false));
$("#cfgForm").addEventListener("input", updateCfgInfo); // "% of time beyond" follows the value being typed
$("#cfgForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const body = {};
  for (const c of CFG) body[c.k] = Number(e.target.elements[c.k].value);
  const r = await fetch("/api/config", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  if (r.ok) S.cfg = await r.json();
  $("#cfgMsg").textContent = r.ok ? `Saved at ${fmtTime(Date.now())}: applied immediately.` : "Not saved: " + (await r.text());
});

// ---------- live header and connection path ----------
function connectLive() {
  const es = new EventSource("/api/live");
  es.onmessage = (m) => renderNow(JSON.parse(m.data));
  es.onerror = () => { $("#status").textContent = "IsThisConnected is not reachable"; showStatus("unreachable"); };
}

// The logo's question mark shows the status, in the header and in the browser tab (favicon).
let faviconKey = "";
function showStatus(status) {
  $("#logo").setAttribute("class", "logo " + status);
  const dark = matchMedia("(prefers-color-scheme: dark)").matches;
  if (status + dark === faviconKey) return;
  faviconKey = status + dark;
  const ink = dark ? "#e6e8eb" : "#1b1f24";
  const q = { slow: css("--slow"), down: css("--down"), paused: css("--paused"), unreachable: css("--paused") }[status] || ink;
  const icon = $("#logo").cloneNode(true); // standalone copy with explicit colors: a favicon has no page CSS
  icon.removeAttribute("id");
  icon.setAttribute("xmlns", "http://www.w3.org/2000/svg");
  icon.querySelectorAll(".ink").forEach((p) => p.setAttribute("stroke", ink));
  icon.querySelectorAll(".q").forEach((p) => p.setAttribute("stroke", q));
  icon.querySelector(".ink-dot").setAttribute("fill", ink);
  $("#favicon").href = "data:image/svg+xml," + encodeURIComponent(new XMLSerializer().serializeToString(icon));
}

// chain: one node per link, from the PC outwards. state: ok | warn | bad | off
function chainNodes(s) {
  const v = s.values || {}, w = s.wifi || {}, f = s.flags || {};
  const ms = (k) => `${num(v[k])} ms`;
  const wired = s.gateway && !s.active_wifi;
  const i1 = v["rtt/8.8.8.8"], i2 = v["rtt/1.1.1.1"];
  return [
    { name: "This PC", value: `CPU ${num(v["pc/cpu"])}%`, state: f.PcBusy ? "bad" : v["pc/cpu"] >= 90 ? "warn" : "ok",
      hint: "Your computer: a saturated CPU delays everything, even on a perfect network." },
    wired ? { name: "Cable", value: "Ethernet", state: "ok", hint: "The PC is on cable: WiFi is not involved." } :
      { name: "WiFi", value: w.connected ? `${w.rssi} dBm · ${num(w.rx_mbps)} Mbit/s` : (w.present ? "disconnected" : "no adapter"),
        state: f.WifiDown || (w.present && !w.connected) ? "bad" : (f.RssiDrop || f.LinkDrop) ? "warn" : w.connected ? "ok" : "off",
        hint: "The radio link between the PC and the access point: signal strength and negotiated speed." },
    { name: "Router", value: s.gateway ? ms("rtt/gateway") : "none", state: !s.gateway || f.GwBad ? "bad" : "ok",
      hint: `Your router / gateway (${s.gateway || "not found"}). Slow here with good WiFi signal = access point or router.` },
    { name: "ISP", value: s.isp_hop ? ms("rtt/isp") : "not found", state: !s.isp_hop ? "off" : f.IspBad ? "warn" : "ok",
      hint: s.isp_hop ? `First router of your Internet provider (${s.isp_hop}). Providers often answer ping slowly on purpose, so this link alone never raises an incident.`
        : "The first router of the provider could not be found (it does not answer traceroute)." },
    { name: "Internet", value: `${num(i1)} / ${num(i2)} ms`, state: f.Inet1Bad && f.Inet2Bad ? "bad" : (f.Inet1Bad || f.Inet2Bad) ? "warn" : "ok",
      hint: "Two independent Internet servers (8.8.8.8 and 1.1.1.1). Both slow = your connection; only one = that server." },
    { name: "DNS", value: ms("dns/system"), state: f.DnsBad ? "bad" : "ok",
      hint: "Turns website names into addresses. Slow DNS = pages that take long to start loading." },
  ];
}

function renderNow(s) {
  const w = s.wifi || {};
  S.bssid = w.connected ? w.bssid : "";
  S.viol = s.violations || {}; S.linkMedian = s.link_median || 0;
  if (!$("#settings").hidden) updateCfgInfo();
  const label = { ok: "Connection OK", slow: "Slowdown in progress", down: "Disconnection in progress", paused: "Monitoring paused" }[s.status];
  showStatus(s.status);
  $("#status").textContent = label;
  $("#btnPause").textContent = s.status === "paused" ? "Resume" : "Pause";
  $("#btnTest").disabled = s.speedtest_running;
  $("#btnTest").textContent = s.speedtest_running ? "Testing…" : "Speed test";
  const metered = s.metered ? '<span class="badge">metered</span>' : "";
  const net = w.connected ? `${esc(w.ssid)}${metered}` : (w.present ? "WiFi not connected" : "Wired network") + metered;
  const items = [
    ["Network", net],
    ["Band", w.connected ? `${esc(w.band || "?")} · ch ${w.channel} · ${esc(w.phy || "")}` : "—"],
    ["Last speed test", s.last_dl ? `${num(s.last_dl, 1)} Mbit/s` : "—"],
    ["Incidents 24 h", String(s.incidents_24h)],
  ];
  $("#now").innerHTML = items.map(([k, x]) => `<div><dt>${k}</dt><dd>${x}</dd></div>`).join("");

  const nodes = chainNodes(s);
  $("#chain").innerHTML = nodes.map((n) => `<li class="node ${n.state}" title="${esc(n.hint)}">
      <span class="name">${n.name}</span><span class="val">${n.value}</span></li>`).join("");
  const first = nodes.find((n) => n.state === "bad");
  let verdict;
  if (s.status === "paused") verdict = "Monitoring is paused.";
  else if (s.open_incident) verdict = `Problem now: <strong>${esc(s.open_incident.label)}</strong> — where: <strong>${esc(s.open_incident.where)}</strong>. ${esc(s.open_incident.why)}`;
  else if (first) verdict = `Getting worse at: <strong>${first.name}</strong> (an incident opens if it lasts 5 s).`;
  else verdict = "All links are fine.";
  if (s.speedtest_running) verdict += " Speed test running: latency checks are paused, because the test itself slows the network.";
  $("#verdict").innerHTML = verdict;
}

// ---------- wiring ----------
document.querySelectorAll("#presets button").forEach((b) => b.addEventListener("click", () => goLive(+b.dataset.span)));
$("#btnLive").addEventListener("click", () => goLive());
$("#rangeForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = new Date($("#from").value).getTime(), t = new Date($("#to").value).getTime();
  if (f < t) setRange(f, t);
});
$("#overlay").addEventListener("change", (e) => { S.overlay = e.target.checked; charts.forEach((c) => c.destroy()); charts = []; drawCharts(); });
$("#btnTest").addEventListener("click", async () => {
  $("#btnTest").disabled = true;
  const r = await fetch("/api/speedtest", { method: "POST" });
  if (!r.ok) alert("Speed test failed: " + (await r.text()));
  load();
});
$("#btnPause").addEventListener("click", () => fetch("/api/pause", { method: "POST" }));
$("#fType").addEventListener("change", renderIncidents);
$("#fWhere").addEventListener("change", renderIncidents);
document.querySelectorAll("#incTable th").forEach((th) => th.addEventListener("click", () => {
  S.sort = { k: th.dataset.k, dir: S.sort.k === th.dataset.k ? -S.sort.dir : -1 };
  renderIncidents();
}));
$("#incTable tbody").addEventListener("click", (e) => { const tr = e.target.closest("tr"); if (tr) showIncident(+tr.dataset.id); });
const zoomSession = (e) => { const tr = e.target.closest("tr"); if (tr) setRange(+tr.dataset.from - 5 * 60e3, +tr.dataset.to + 5 * 60e3); };
$("#sessTable tbody").addEventListener("click", zoomSession);
$("#sessTable tbody").addEventListener("keydown", (e) => { if (e.key === "Enter") zoomSession(e); });
$("#incTable tbody").addEventListener("keydown", (e) => { if (e.key === "Enter") { const tr = e.target.closest("tr"); if (tr) showIncident(+tr.dataset.id); } });
document.addEventListener("keydown", (e) => {
  if (e.target.closest("input, select, textarea")) return;
  if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
  const [f, t] = currentRange(), shift = (t - f) * 0.25 * (e.key === "ArrowLeft" ? -1 : 1);
  setRange(f + shift, t + shift);
});
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => { charts.forEach((c) => c.destroy()); charts = []; drawCharts(); });

const showError = (msg) => { $("#rangeInfo").textContent = "Error: " + msg; };
window.addEventListener("error", (e) => showError(e.message));
window.addEventListener("unhandledrejection", (e) => showError(e.reason?.message || String(e.reason)));

syncControls();
fetch("/api/config").then((r) => r.json()).then((c) => { S.cfg = c; updateCfgInfo(); });
load();
connectLive();
setInterval(() => { if (S.live) { syncControls(); load(); } }, 5000);
