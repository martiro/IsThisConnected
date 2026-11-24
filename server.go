package main

import (
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"
)

const listenAddr = "127.0.0.1:8777"

//go:embed web
var webFS embed.FS

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// rangeParams reads from/to (ms epoch); default last hour.
func rangeParams(r *http.Request) (int64, int64, bool) {
	to, err1 := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	from, err2 := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if err1 != nil || err2 != nil {
		n := now()
		return n - 3600_000, n, true
	}
	return from, to, from < to && to-from <= 400*24*3600_000
}

func (a *App) liveSnapshot() map[string]any {
	a.st.mu.Lock()
	vals := make(map[string]float64, len(a.st.vals))
	for k, v := range a.st.vals {
		vals[k] = v
	}
	snap := map[string]any{"ts": now(), "values": vals, "wifi": a.st.Wifi, "gateway": a.st.Gateway, "metered": a.st.Metered,
		"last_dl": a.st.LastDL, "last_dl_at": a.st.LastDLAt, "active_wifi": a.st.ActiveIsWifi, "isp_hop": a.st.IspHop}
	a.st.mu.Unlock()
	snap["status"] = a.status()
	if m := a.linkMedian.Load().(float64); !math.IsNaN(m) {
		snap["link_median"] = m // usual WiFi link speed (24 h): the link_rate_pct threshold is a share of it
	}
	snap["speedtest_running"] = a.speedBusy.Load()
	snap["incidents_24h"] = a.store.Count24h(time.Now())
	a.detMu.Lock()
	if in := a.det.Cur; in != nil {
		c := *in
		c.describe()
		snap["open_incident"] = c
	}
	snap["flags"] = a.lastTick.Flags
	snap["violations"] = a.lastTick.Viol // metric -> value currently over its threshold
	a.detMu.Unlock()
	return snap
}

func (a *App) serve() {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("GET /api/series", func(w http.ResponseWriter, r *http.Request) {
		from, to, ok := rangeParams(r)
		if !ok {
			http.Error(w, "invalid range", 400)
			return
		}
		res, err := a.store.Series(from, to)
		writeJSON(w, res, err)
	})
	mux.HandleFunc("GET /api/incidents", func(w http.ResponseWriter, r *http.Request) {
		from, to, ok := rangeParams(r)
		if !ok {
			http.Error(w, "invalid range", 400)
			return
		}
		res, err := a.store.Incidents(from, to)
		writeJSON(w, res, err)
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		from, to, ok := rangeParams(r)
		if !ok {
			http.Error(w, "invalid range", 400)
			return
		}
		res, err := a.store.Events(from, to)
		writeJSON(w, res, err)
	})
	mux.HandleFunc("GET /api/aps", func(w http.ResponseWriter, r *http.Request) {
		from, to, ok := rangeParams(r)
		if !ok {
			http.Error(w, "invalid range", 400)
			return
		}
		res, err := a.store.APSessions(from, to)
		writeJSON(w, res, err)
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.config(), nil)
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		n := a.config() // fields missing from the body keep their value
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&n); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), 400)
			return
		}
		if err := a.setThresholds(n); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, a.config(), nil)
	})
	mux.HandleFunc("GET /api/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fl, _ := w.(http.Flusher)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			b, _ := json.Marshal(a.liveSnapshot())
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-a.ctx.Done():
				return
			case <-t.C:
			}
		}
	})
	mux.HandleFunc("POST /api/speedtest", func(w http.ResponseWriter, r *http.Request) {
		mbps, err := a.speedtest(true)
		writeJSON(w, map[string]float64{"mbps": mbps}, err)
	})
	mux.HandleFunc("POST /api/pause", func(w http.ResponseWriter, r *http.Request) {
		a.togglePause()
		writeJSON(w, map[string]bool{"paused": a.paused.Load()}, nil)
	})
	mux.HandleFunc("GET /api/export.csv", func(w http.ResponseWriter, r *http.Request) {
		from, to, ok := rangeParams(r)
		if !ok {
			http.Error(w, "invalid range", 400)
			return
		}
		a.exportCSV(w, from, to)
	})
	srv := &http.Server{Addr: listenAddr, Handler: localOnly(mux)}
	go func() { <-a.ctx.Done(); srv.Close() }()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server: %v", err)
	}
}

// localOnly rejects requests whose Host is not ours (DNS-rebinding guard for a localhost API).
func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != listenAddr && r.Host != "localhost:8777" {
			http.Error(w, "forbidden", 403)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (a *App) exportCSV(w http.ResponseWriter, from, to int64) {
	raw := useRaw(from, to, time.Now())
	q := `SELECT s.key, x.ts, x.value FROM samples x JOIN series s ON s.id = x.sid WHERE x.ts BETWEEN ? AND ? ORDER BY x.ts`
	if !raw {
		q = `SELECT s.key, x.ts, x.avg FROM agg_1m x JOIN series s ON s.id = x.sid WHERE x.ts BETWEEN ? AND ? ORDER BY x.ts`
	}
	rows, err := a.store.db.Query(q, from, to)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="isthisconnected.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"timestamp", "metric", "value"})
	for rows.Next() {
		var k string
		var ts int64
		var v float64
		if rows.Scan(&k, &ts, &v) == nil {
			cw.Write([]string{time.UnixMilli(ts).Format(time.RFC3339), k, strconv.FormatFloat(v, 'f', 3, 64)})
		}
	}
	cw.Flush()
}

func (a *App) togglePause() {
	p := !a.paused.Load()
	a.paused.Store(p)
	if p {
		a.detMu.Lock()
		if in := a.det.Cur; in != nil { // a pause must not leave an incident open forever
			in.End = now()
			a.store.UpdateIncident(in)
		}
		c := a.config()
		a.det = &Detector{OpenAfter: c.OpenAfterSec, CloseAfter: c.CloseAfterSec}
		a.detMu.Unlock()
	}
	a.event("pause", strconv.FormatBool(p))
	if a.onChange != nil {
		a.onChange()
	}
}
