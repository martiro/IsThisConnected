package main

// SQLite persistence: raw samples (7 days), 1-minute aggregates (1 year), incidents and events (forever).

import (
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS series(id INTEGER PRIMARY KEY, key TEXT UNIQUE NOT NULL);
CREATE TABLE IF NOT EXISTS samples(sid INTEGER, ts INTEGER, value REAL, PRIMARY KEY(sid, ts)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS agg_1m(sid INTEGER, ts INTEGER, min REAL, avg REAL, max REAL, p95 REAL, count INTEGER, PRIMARY KEY(sid, ts)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS incidents(id INTEGER PRIMARY KEY, start INTEGER, end INTEGER, type TEXT, trigger TEXT, metrics TEXT,
  peak REAL, diagnosis TEXT, reason_code INTEGER, reason TEXT, ssid TEXT, bssid TEXT, snapshot TEXT);
CREATE INDEX IF NOT EXISTS incidents_start ON incidents(start);
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY, ts INTEGER, kind TEXT, detail TEXT);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
`

const (
	rawRetention = 7 * 24 * time.Hour
	aggRetention = 365 * 24 * time.Hour
	rawMaxSpan   = 6 * time.Hour
	maxPoints    = 2000
)

type sample struct {
	sid   int64
	ts    int64
	value float64
}

type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	sids map[string]int64
	keys map[int64]string
	buf  []sample
	agg  map[[2]int64][]float64 // (sid, minute) -> values
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-1024)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	migrateLegacyKeys(db)
	s := &Store{db: db, sids: map[string]int64{}, keys: map[int64]string{}, agg: map[[2]int64][]float64{}}
	rows, err := db.Query(`SELECT id, key FROM series`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var k string
		rows.Scan(&id, &k)
		s.sids[k], s.keys[id] = id, k
	}
	return s, nil
}

func (s *Store) sid(key string) int64 { // caller holds s.mu
	if id, ok := s.sids[key]; ok {
		return id
	}
	r, err := s.db.Exec(`INSERT INTO series(key) VALUES(?)`, key)
	if err != nil {
		return -1
	}
	id, _ := r.LastInsertId()
	s.sids[key], s.keys[id] = id, key
	return id
}

// Add buffers one sample; it is written by Flush.
func (s *Store) Add(key string, ts int64, v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.sid(key)
	if id < 0 {
		return
	}
	s.buf = append(s.buf, sample{id, ts, v})
	k := [2]int64{id, ts / 60000 * 60000}
	s.agg[k] = append(s.agg[k], v)
}

// Flush writes buffered samples and the aggregates of completed minutes (all of them if final).
func (s *Store) Flush(final bool) error {
	s.mu.Lock()
	buf := s.buf
	s.buf = nil
	curMin := time.Now().UnixMilli() / 60000 * 60000
	done := map[[2]int64][]float64{}
	for k, v := range s.agg {
		if final || k[1] < curMin {
			done[k] = v
			delete(s.agg, k)
		}
	}
	s.mu.Unlock()
	if len(buf) == 0 && len(done) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	ins, _ := tx.Prepare(`INSERT OR REPLACE INTO samples(sid, ts, value) VALUES(?,?,?)`)
	for _, x := range buf {
		ins.Exec(x.sid, x.ts, x.value)
	}
	ag, _ := tx.Prepare(`INSERT OR REPLACE INTO agg_1m(sid, ts, min, avg, max, p95, count) VALUES(?,?,?,?,?,?,?)`)
	for k, v := range done {
		sort.Float64s(v)
		sum := 0.0
		for _, x := range v {
			sum += x
		}
		ag.Exec(k[0], k[1], v[0], sum/float64(len(v)), v[len(v)-1], percentile(v, 0.95), len(v))
	}
	return tx.Commit()
}

func (s *Store) Cleanup(now time.Time) {
	s.db.Exec(`DELETE FROM samples WHERE ts < ?`, now.Add(-rawRetention).UnixMilli())
	s.db.Exec(`DELETE FROM agg_1m WHERE ts < ?`, now.Add(-aggRetention).UnixMilli())
	s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
}

// useRaw decides whether a range is served from raw samples or from 1-minute aggregates.
func useRaw(from, to int64, now time.Time) bool {
	return to-from <= rawMaxSpan.Milliseconds() && from >= now.Add(-rawRetention).UnixMilli()
}

// bucketStep returns the bucket size (ms) that keeps a range under maxPoints.
func bucketStep(from, to int64, raw bool) int64 {
	min := int64(1000)
	if !raw {
		min = 60000
	}
	step := (to - from) / maxPoints
	if step < min {
		return min
	}
	return (step + min - 1) / min * min
}

type SeriesResult struct {
	X      []float64             `json:"x"` // seconds (uPlot)
	Series map[string][]*float64 `json:"series"`
	Raw    bool                  `json:"raw"`
	Step   int64                 `json:"step"`
}

// Series returns all series aligned on a common bucketed time axis.
func (s *Store) Series(from, to int64) (*SeriesResult, error) {
	raw := useRaw(from, to, time.Now())
	step := bucketStep(from, to, raw)
	q := `SELECT sid, ts/?1*?1 AS b, avg(value) FROM samples WHERE ts BETWEEN ?2 AND ?3 GROUP BY sid, b`
	if !raw {
		q = `SELECT sid, ts/?1*?1 AS b, sum(avg*count)/sum(count) FROM agg_1m WHERE ts BETWEEN ?2 AND ?3 GROUP BY sid, b`
	}
	rows, err := s.db.Query(q, step, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type pt struct {
		b int64
		v float64
	}
	data := map[int64][]pt{}
	buckets := map[int64]bool{}
	for rows.Next() {
		var sid, b int64
		var v float64
		if rows.Scan(&sid, &b, &v) == nil {
			data[sid] = append(data[sid], pt{b, v})
			buckets[b] = true
		}
	}
	xs := make([]int64, 0, len(buckets))
	for b := range buckets {
		xs = append(xs, b)
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	idx := map[int64]int{}
	res := &SeriesResult{X: make([]float64, len(xs)), Series: map[string][]*float64{}, Raw: raw, Step: step}
	for i, b := range xs {
		idx[b] = i
		res.X[i] = float64(b) / 1000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for sid, pts := range data {
		if strings.HasPrefix(s.keys[sid], "ap/") { // per access point history: only for the AP table
			continue
		}
		col := make([]*float64, len(xs))
		for _, p := range pts {
			v := p.v
			col[idx[p.b]] = &v
		}
		res.Series[s.keys[sid]] = col
	}
	return res, nil
}

// Median of a series' minute averages since `since` (used for link-rate and download baselines).
func (s *Store) MedianSince(key string, since int64, minCount int) (float64, bool) {
	s.mu.Lock()
	id, ok := s.sids[key]
	s.mu.Unlock()
	if !ok {
		return 0, false
	}
	rows, err := s.db.Query(`SELECT avg FROM agg_1m WHERE sid=? AND ts>=?`, id, since)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	var v []float64
	for rows.Next() {
		var x float64
		rows.Scan(&x)
		v = append(v, x)
	}
	if len(v) < minCount {
		return 0, false
	}
	return median(v), true
}

// Last returns the most recent stored sample of a series (0, 0 if none).
func (s *Store) Last(key string) (ts int64, v float64) {
	s.db.QueryRow(`SELECT x.ts, x.value FROM samples x JOIN series s ON s.id = x.sid WHERE s.key = ? ORDER BY x.ts DESC LIMIT 1`, key).Scan(&ts, &v)
	return
}

func (s *Store) InsertIncident(in *Incident) {
	r, err := s.db.Exec(`INSERT INTO incidents(start, end, type, trigger, metrics, peak, diagnosis, reason_code, reason, ssid, bssid, snapshot)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, in.Start, nullIf0(in.End), in.Type, in.Trigger, strings.Join(in.Metrics, ","), in.Peak,
		in.Diagnosis, in.ReasonCode, in.Reason, in.SSID, in.BSSID, in.Snapshot)
	if err == nil {
		in.ID, _ = r.LastInsertId()
	}
}

func (s *Store) UpdateIncident(in *Incident) {
	s.db.Exec(`UPDATE incidents SET end=?, type=?, metrics=?, peak=?, diagnosis=?, reason_code=?, reason=? WHERE id=?`,
		nullIf0(in.End), in.Type, strings.Join(in.Metrics, ","), in.Peak, in.Diagnosis, in.ReasonCode, in.Reason, in.ID)
}

func nullIf0(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (s *Store) Incidents(from, to int64) ([]Incident, error) {
	rows, err := s.db.Query(`SELECT id, start, coalesce(end,0), type, trigger, metrics, peak, diagnosis, reason_code, reason, ssid, bssid, snapshot
		FROM incidents WHERE start <= ? AND coalesce(end, 9e18) >= ? ORDER BY start DESC LIMIT 5000`, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		var in Incident
		var m string
		if rows.Scan(&in.ID, &in.Start, &in.End, &in.Type, &in.Trigger, &m, &in.Peak, &in.Diagnosis, &in.ReasonCode, &in.Reason, &in.SSID, &in.BSSID, &in.Snapshot) == nil {
			in.Metrics = strings.Split(m, ",")
			in.describe()
			out = append(out, in)
		}
	}
	return out, nil
}

// CloseDangling closes incidents left open by a crash or shutdown at the last sample written before it
// (runs at startup, before any new sample), so they keep a realistic duration.
func (s *Store) CloseDangling() {
	s.db.Exec(`UPDATE incidents SET end = max(start, coalesce((SELECT max(ts) FROM samples), start)) WHERE end IS NULL`)
}

type Event struct {
	TS     int64  `json:"ts"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

func (s *Store) AddEvent(ts int64, kind, detail string) {
	s.db.Exec(`INSERT INTO events(ts, kind, detail) VALUES(?,?,?)`, ts, kind, detail)
}

func (s *Store) Events(from, to int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT ts, kind, detail FROM events WHERE ts BETWEEN ? AND ? ORDER BY ts LIMIT 5000`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if rows.Scan(&e.TS, &e.Kind, &e.Detail) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Store) Count24h(now time.Time) (n int) {
	s.db.QueryRow(`SELECT count(*) FROM incidents WHERE start >= ?`, now.Add(-24*time.Hour).UnixMilli()).Scan(&n)
	return
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

type APSession struct {
	BSSID string  `json:"bssid"`
	SSID  string  `json:"ssid"`
	Start int64   `json:"start"`
	End   int64   `json:"end"`    // last sample seen on this AP
	Next  string  `json:"next"`   // BSSID connected right after (roaming), "" = connection lost or no data
	AvgMs float64 `json:"avg_ms"` // ping to the router during this connection
	P95Ms float64 `json:"p95_ms"` // slowest 5% of those pings
	vals  []float64
	n     float64
}

// apPoint: one per-AP sample: a ping (raw) or a minute aggregate (avg, p95, count).
type apPoint struct {
	ts          int64
	bssid, ssid string
	v, p95, n   float64
}

func splitAPKey(k string) (bssid, ssid string) {
	parts := strings.SplitN(strings.TrimPrefix(k, "ap/"), "/", 2) // a BSSID has no '/'
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

// apSessions groups time-ordered per-AP samples into connections: a new connection starts when the AP changes
// or after a gap longer than maxGap. A switch within maxGap is recorded as roaming (Next).
// raw: points are single pings (p95 computed from them); otherwise minute aggregates (p95 = mean of minute p95s).
func apSessions(pts []apPoint, maxGap int64, raw bool) []APSession {
	out := []APSession{}
	for _, p := range pts {
		n := len(out)
		if n > 0 && p.ts-out[n-1].End <= maxGap {
			if cur := &out[n-1]; cur.BSSID == p.bssid && cur.SSID == p.ssid {
				cur.End = p.ts
				cur.add(p, raw)
				continue
			}
			out[n-1].Next = p.bssid
		}
		out = append(out, APSession{BSSID: p.bssid, SSID: p.ssid, Start: p.ts, End: p.ts})
		out[len(out)-1].add(p, raw)
	}
	for i := range out {
		x := &out[i]
		x.AvgMs /= x.n
		if raw {
			sort.Float64s(x.vals)
			x.P95Ms = percentile(x.vals, 0.95)
		} else {
			x.P95Ms /= float64(len(x.vals))
		}
		x.vals = nil
	}
	return out
}

func (x *APSession) add(p apPoint, raw bool) {
	x.AvgMs += p.v * p.n
	x.n += p.n
	if raw {
		x.vals = append(x.vals, p.v)
	} else {
		x.vals = append(x.vals, p.p95) // only its length is used
		x.P95Ms += p.p95
	}
}

// APSessions lists the connections to each access point in the range (per-second data when available).
func (s *Store) APSessions(from, to int64) ([]APSession, error) {
	raw := useRaw(from, to, time.Now())
	q, gap := `SELECT s.key, x.ts, x.value, x.value, 1 FROM samples x JOIN series s ON s.id = x.sid WHERE s.key LIKE 'ap/%' AND x.ts BETWEEN ? AND ? ORDER BY x.ts`, int64(30_000)
	if !raw { // long ranges: minute data, start/end accurate to the minute
		q, gap = `SELECT s.key, x.ts, x.avg, x.p95, x.count FROM agg_1m x JOIN series s ON s.id = x.sid WHERE s.key LIKE 'ap/%' AND x.ts BETWEEN ? AND ? ORDER BY x.ts`, 180_000
	}
	rows, err := s.db.Query(q, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pts []apPoint
	for rows.Next() {
		var p apPoint
		var k string
		if rows.Scan(&k, &p.ts, &p.v, &p.p95, &p.n) == nil {
			p.bssid, p.ssid = splitAPKey(k)
			pts = append(pts, p)
		}
	}
	return apSessions(pts, gap, raw), nil
}

// legacyKeys maps the Italian keys written by early development builds to the current English ones.
var legacyKeys = [][2]string{
	{"motivo_disconnessione", "disconnect_reason"},
	{"disconnessione_wifi", "wifi_disconnect"},
	{"disconnessione", "disconnection"},
	{"rallentamento", "slowdown"},
	{"pc_sovraccarico", "pc_overload"},
	{"rete_cablata", "wired_lan"},
	{"segnale_debole", "weak_signal"},
	{"interferenza", "ap_congestion"},
	{"degrado_link", "link_degraded"},
	{"tcp_lento", "tcp_slow"},
	{"banda_ridotta", "low_bandwidth"},
	{"host_singolo", "single_host"},
	{"altro", "other"},
	{"wifi_connessa", "wifi_connected"},
	{"wifi_disconnessa", "wifi_disconnected"},
	{"canale", "channel"},
	{"a_consumo", "metered"},
	{"pausa", "pause"},
	{"valori", "values"},
	{"wifi_stato", "wifi_state"},
	{"gateway_assente", "no_gateway"},
	{"dns_sistema", "dns_system"},
}

// migrateLegacyKeys renames, in place, keys stored by early development builds (idempotent, cheap when
// there is nothing to do). shortcut: only the original author's database needs it; drop it after a release or two.
func migrateLegacyKeys(db *sql.DB) {
	for _, k := range legacyKeys {
		db.Exec(`UPDATE incidents SET diagnosis = ? WHERE diagnosis = ?`, k[1], k[0])
		db.Exec(`UPDATE incidents SET type = ? WHERE type = ?`, k[1], k[0])
		db.Exec(`UPDATE events SET kind = ? WHERE kind = ?`, k[1], k[0])
	}
	db.Exec(`UPDATE series SET key = 'dns/system' WHERE key = 'dns/sistema'`)
	for _, col := range []string{"trigger", "metrics"} { // metric keys: comma-separated list in metrics
		db.Exec(`UPDATE incidents SET ` + col + ` = replace(replace(replace(replace(` + col + `,
			'perdita_', 'loss_'), 'wifi_stato', 'wifi_state'), 'gateway_assente', 'no_gateway'), 'dns_sistema', 'dns_system')
			WHERE ` + col + ` LIKE '%perdita_%' OR ` + col + ` LIKE '%wifi_stato%' OR ` + col + ` LIKE '%gateway_assente%' OR ` + col + ` LIKE '%dns_sistema%'`)
	}
	db.Exec(`UPDATE incidents SET snapshot = replace(replace(snapshot, '"valori":', '"values":'), 'dns/sistema', 'dns/system')
		WHERE snapshot LIKE '%"valori":%' OR snapshot LIKE '%dns/sistema%'`)
}
