package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/eshaan2418/riskd/internal/deribit"
	"github.com/eshaan2418/riskd/internal/engine"
	"github.com/eshaan2418/riskd/internal/sim"
)

// fakeDeribit serves a REST summary and a WebSocket that streams synthetic frames, interleaved
// with control frames (subscribe response, heartbeat test_request) like the real exchange.
func fakeDeribit(t *testing.T) *httptest.Server {
	m := sim.NewMarket(42, 5, 11)
	summary := m.Summary()
	var frames [][]byte
	frames = append(frames, []byte(`{"jsonrpc":"2.0","id":1,"result":["markprice.options.btc_usd","deribit_price_index.btc_usd"]}`))
	for s := 0; s < 300; s++ {
		for _, ev := range m.Step(1000, 0.3) {
			frames = append(frames, sim.Frame(ev))
		}
		if s%50 == 0 {
			frames = append(frames, []byte(`{"jsonrpc":"2.0","method":"heartbeat","params":{"type":"test_request"}}`))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/public/get_book_summary_by_currency", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(summary) })
	mux.HandleFunc("/ws/api/v2", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		go func() { // drain client messages (subscribe, heartbeat replies)
			for {
				if _, _, err := c.Read(ctx); err != nil {
					return
				}
			}
		}()
		for _, f := range frames {
			if err := c.Write(ctx, websocket.MessageText, f); err != nil {
				return
			}
		}
		_ = c.Close(websocket.StatusNormalClosure, "done")
	})
	return httptest.NewServer(mux)
}

func TestLiveEqualsReplay(t *testing.T) {
	srv := fakeDeribit(t)
	defer srv.Close()
	deribit.RESTURL = srv.URL + "/api/v2/public/get_book_summary_by_currency?currency=%s&kind=option"
	deribit.WSURL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/api/v2"
	journal := filepath.Join(t.TempDir(), "live.jsonl.gz")
	mk := func() *engine.Runner {
		fs := newFlagSet()
		ef := addEngineFlags(fs)
		_ = fs.Parse([]string{"-positions", "30", "-trade-every", "5s", "-hedge-band", "1"})
		cfg, err := ef.config()
		if err != nil {
			t.Fatal(err)
		}
		return engine.NewRunner(engine.New(cfg), true)
	}
	live := mk()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runLive(ctx, "BTC", journal, live); err != nil {
		t.Fatal(err)
	}
	ls := live.Snapshot()
	if ls.Stats.Events < 600 || ls.Stats.Trades < 30 {
		t.Fatalf("live run too small: %+v", ls.Stats)
	}
	rep := mk()
	if err := replayInto(context.Background(), journal, rep, 0); err != nil {
		t.Fatal(err)
	}
	rs := rep.Snapshot()
	if ls.Hash != rs.Hash || ls.Stats != rs.Stats {
		t.Fatalf("live %s %+v\nreplay %s %+v", ls.Hash, ls.Stats, rs.Hash, rs.Stats)
	}
	t.Logf("live == replay: %d events, %d trades, hash %s", ls.Stats.Events, ls.Stats.Trades, ls.Hash)
}
