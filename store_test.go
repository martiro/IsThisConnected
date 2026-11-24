package main

import (
	"fmt"
	"testing"
	"time"
)

func TestUseRawAndStep(t *testing.T) {
	n := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return n.Add(-d).UnixMilli() }
	if !useRaw(ms(time.Hour), n.UnixMilli(), n) {
		t.Error("1h should be raw")
	}
	if useRaw(ms(7*time.Hour), n.UnixMilli(), n) {
		t.Error("7h should be aggregated")
	}
	if useRaw(ms(8*24*time.Hour), ms(8*24*time.Hour-time.Hour), n) {
		t.Error("older than raw retention should be aggregated")
	}
	if s := bucketStep(ms(15*time.Minute), n.UnixMilli(), true); s != 1000 {
		t.Errorf("15m step = %d", s)
	}
	if s := bucketStep(ms(30*24*time.Hour), n.UnixMilli(), false); s%60000 != 0 || (30*24*3600_000)/s > maxPoints {
		t.Errorf("30d step = %d", s)
	}
}

func TestAPSessions(t *testing.T) {
	at := func(b string, ts int64, v float64) apPoint {
		return apPoint{bssid: b, ssid: "net", ts: ts, v: v, p95: v, n: 1}
	}
	pts := []apPoint{at("aa", 0, 2), at("aa", 1000, 4), at("aa", 2000, 30), at("bb", 5000, 5), at("bb", 6000, 5), at("bb", 100_000, 9), at("aa", 101_000, 1)}
	got := apSessions(pts, 30_000, true)
	want := []APSession{
		{BSSID: "aa", SSID: "net", Start: 0, End: 2000, Next: "bb", AvgMs: 12, P95Ms: 30}, // roaming A -> B
		{BSSID: "bb", SSID: "net", Start: 5000, End: 6000, AvgMs: 5, P95Ms: 5},            // then 94 s without data: connection lost
		{BSSID: "bb", SSID: "net", Start: 100_000, End: 100_000, Next: "aa", AvgMs: 9, P95Ms: 9},
		{BSSID: "aa", SSID: "net", Start: 101_000, End: 101_000, AvgMs: 1, P95Ms: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sessions: %+v", len(got), got)
	}
	for i := range want {
		g := got[i]
		g.n = 0
		if fmt.Sprint(g) != fmt.Sprint(want[i]) {
			t.Errorf("session %d = %+v, want %+v", i, g, want[i])
		}
	}
	// minute aggregates: weighted average, mean of the minute p95s
	agg := []apPoint{{bssid: "aa", ts: 0, v: 2, p95: 10, n: 60}, {bssid: "aa", ts: 60_000, v: 8, p95: 20, n: 20}}
	if g := apSessions(agg, 180_000, false); len(g) != 1 || g[0].AvgMs != 3.5 || g[0].P95Ms != 15 {
		t.Errorf("aggregated session = %+v", g)
	}
	if b, s := splitAPKey("ap/02:00:00:00:00:01/My/Net"); b != "02:00:00:00:00:01" || s != "My/Net" {
		t.Errorf("splitAPKey = %q %q", b, s)
	}
}

func TestCloseDanglingKeepsDuration(t *testing.T) {
	st, err := openStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	st.Add("rtt/gateway", 1000, 5)
	st.Add("rtt/gateway", 61000, 5) // last sample before the "crash"
	st.Flush(true)
	in := &Incident{Start: 20000, Type: "slowdown", Diagnosis: "ap_congestion"}
	st.InsertIncident(in) // still open (End = 0) when the process dies
	st.CloseDangling()
	got, _ := st.Incidents(0, 100000)
	if len(got) != 1 || got[0].End != 61000 {
		t.Fatalf("dangling incident closed at %+v, want end 61000", got)
	}
}

func TestMigrateLegacyKeys(t *testing.T) {
	st, err := openStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	st.db.Exec(`INSERT INTO incidents(start, type, trigger, metrics, diagnosis, snapshot) VALUES(1, 'rallentamento', 'perdita_gateway',
		'perdita_gateway,dns_sistema,wifi_stato', 'interferenza', '{"valori":{"dns/sistema":3}}')`)
	st.db.Exec(`INSERT INTO events(ts, kind, detail) VALUES(1, 'wifi_disconnessa', 'x')`)
	// low-bandwidth incidents store the bare values map, without the "valori" wrapper
	st.db.Exec(`INSERT INTO incidents(start, type, trigger, metrics, diagnosis, snapshot) VALUES(2, 'rallentamento', 'download', 'download', 'banda_ridotta', '{"dns/sistema":3}')`)
	st.db.Exec(`INSERT INTO series(key) VALUES('dns/sistema')`)
	migrateLegacyKeys(st.db)
	migrateLegacyKeys(st.db) // idempotent
	var typ, trig, metrics, diag, snap, kind, key string
	st.db.QueryRow(`SELECT type, trigger, metrics, diagnosis, snapshot FROM incidents WHERE start = 1`).Scan(&typ, &trig, &metrics, &diag, &snap)
	st.db.QueryRow(`SELECT kind FROM events`).Scan(&kind)
	st.db.QueryRow(`SELECT key FROM series`).Scan(&key)
	var snap2, diag2 string
	st.db.QueryRow(`SELECT snapshot, diagnosis FROM incidents WHERE start = 2`).Scan(&snap2, &diag2)
	got := []string{typ, trig, metrics, diag, snap, kind, key, snap2, diag2}
	want := []string{"slowdown", "loss_gateway", "loss_gateway,dns_system,wifi_state", "ap_congestion", `{"values":{"dns/system":3}}`, "wifi_disconnected", "dns/system", `{"dns/system":3}`, "low_bandwidth"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %q, want %q", got[i], want[i])
		}
	}
}
