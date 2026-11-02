// Package server exposes the risk engine over HTTP: JSON snapshots, a Server-Sent Events stream
// and Prometheus-format metrics. Every handler reads an immutable snapshot; none touches engine
// state.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/eshaan2418/riskd/internal/engine"
)

// Source provides snapshots.
type Source interface {
	Snapshot() *engine.Snapshot
	Subscribe() (<-chan *engine.Snapshot, func())
}

// Handler returns the HTTP mux.
func Handler(src Source) http.Handler {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	}
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		s := *src.Snapshot()
		s.Positions = nil
		j(w, s)
	})
	mux.HandleFunc("GET /api/positions", func(w http.ResponseWriter, _ *http.Request) { j(w, src.Snapshot().Positions) })
	mux.HandleFunc("GET /api/scenarios", func(w http.ResponseWriter, _ *http.Request) { j(w, src.Snapshot().Scenarios) })
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, _ *http.Request) {
		s := src.Snapshot()
		j(w, map[string]any{"limits": s.Limits, "alerts": s.Alerts})
	})
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ch, cancel := src.Subscribe()
		defer cancel()
		// Send headers now: clients see the stream open before the first event arrives.
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
			case s := <-ch:
				b, _ := json.Marshal(struct {
					Seq    uint64         `json:"seq"`
					TS     int64          `json:"ts"`
					Index  float64        `json:"index"`
					Totals engine.Totals  `json:"totals"`
					PnL    engine.PnLView `json:"pnl"`
					Alerts int            `json:"alerts"`
				}{s.Seq, s.TS, s.Index, s.Totals, s.PnL, len(s.Alerts)})
				fmt.Fprintf(w, "event: risk\ndata: %s\n\n", b)
				fl.Flush()
			}
		}
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		s := src.Snapshot()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		g := func(name, help string, v float64) {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, v)
		}
		c := func(name, help string, v uint64) {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
		}
		g("riskd_delta_btc", "Book delta vs index (BTC).", s.Totals.DeltaBTC)
		g("riskd_gamma_1pct_btc", "Delta change per 1% index move (BTC).", s.Totals.Gamma1PctBTC)
		g("riskd_vega_usd", "Vega (USD per vol point).", s.Totals.VegaUSD)
		g("riskd_theta_usd_day", "Theta (USD per day).", s.Totals.ThetaUSDDay)
		g("riskd_pnl_usd", "Total PnL (USD).", s.PnL.Total)
		g("riskd_pnl_unexplained_usd", "PnL not explained by the second-order Greeks (USD).", s.PnL.Explain.Unexplained)
		if s.Scenarios != nil {
			g("riskd_scenario_worst_usd", "Worst scenario-grid PnL (USD).", s.Scenarios.Worst)
		}
		for _, l := range s.Limits {
			fmt.Fprintf(w, "riskd_limit_utilisation{limit=%q} %g\n", l.Name, l.Util)
		}
		c("riskd_events_total", "Events applied.", s.Stats.Events)
		c("riskd_mark_rows_total", "Option mark rows applied.", s.Stats.MarkRows)
		c("riskd_revaluations_total", "Position revaluations.", s.Stats.Revalued)
		c("riskd_iv_fallback_total", "Revaluations using Deribit mark IV (own inversion failed).", s.Stats.IVFallback)
		c("riskd_trades_total", "Simulated option fills.", s.Stats.Trades)
		if s.Latency != nil {
			g("riskd_event_latency_p99_us", "Event processing latency p99 (microseconds).", s.Latency.P99us)
		}
	})
	return mux
}
