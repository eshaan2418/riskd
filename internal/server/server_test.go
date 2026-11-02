package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eshaan2418/riskd/internal/engine"
	"github.com/eshaan2418/riskd/internal/pricing"
	"github.com/eshaan2418/riskd/internal/sim"
)

func setup(t *testing.T) (*engine.Runner, *sim.Market, *httptest.Server) {
	t.Helper()
	e := engine.New(engine.Config{Pricer: pricing.Go{}, KeepPositions: true, Limits: engine.Limits{MaxDeltaBTC: 1},
		Strategy: &engine.DemoBook{Seed: 1, Positions: 20, HalfSpreadFrac: 0.01, MinHalfSpreadBTC: 1e-4}})
	r := engine.NewRunner(e, false)
	m := sim.NewMarket(1, 4, 9)
	for _, ev := range m.Step(0, 1) {
		r.Step(&ev)
	}
	return r, m, httptest.NewServer(Handler(r))
}

func TestEndpoints(t *testing.T) {
	r, _, srv := setup(t)
	defer srv.Close()
	var s engine.Snapshot
	resp, err := http.Get(srv.URL + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if s.NumPositions != 20 || s.Hash != r.Snapshot().Hash || len(s.Positions) != 0 || s.Scenarios == nil {
		t.Fatalf("snapshot: positions %d hash %s rows %d", s.NumPositions, s.Hash, len(s.Positions))
	}
	var rows []engine.PositionRow
	resp, _ = http.Get(srv.URL + "/api/positions")
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	resp.Body.Close()
	if len(rows) != 20 {
		t.Fatalf("positions: %d", len(rows))
	}
	resp, _ = http.Get(srv.URL + "/metrics")
	sc := bufio.NewScanner(resp.Body)
	found := 0
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "riskd_delta_btc ") || strings.HasPrefix(sc.Text(), `riskd_limit_utilisation{limit="delta_btc"}`) {
			found++
		}
	}
	resp.Body.Close()
	if found != 2 {
		t.Fatalf("metrics lines found: %d", found)
	}
}

// The SSE stream delivers snapshots as the engine advances, newest-wins under back-pressure.
func TestStream(t *testing.T) {
	r, m, srv := setup(t)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go func() {
		for i := 0; i < 50; i++ {
			for _, ev := range m.Step(1000, 0.5) {
				r.Step(&ev)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	sc := bufio.NewScanner(resp.Body)
	var last uint64
	got := 0
	for sc.Scan() && got < 5 {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var v struct {
			Seq uint64 `json:"seq"`
		}
		if err := json.Unmarshal([]byte(line[6:]), &v); err != nil {
			t.Fatal(err)
		}
		if v.Seq <= last {
			t.Fatalf("sequence went backwards: %d after %d", v.Seq, last)
		}
		last = v.Seq
		got++
	}
	if got < 5 {
		t.Fatalf("received %d events", got)
	}
}
