//go:build volsurf

package main

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"github.com/eshaan2418/riskd/internal/engine"
)

// The committed sample is the journal of a real 10-minute live session against Deribit;
// research/results/live_10m.json is that live run's own output. Replaying the journal with the
// default flags must reproduce the live state hash exactly. Floating-point results are only
// bit-reproducible on the same architecture (compilers may fuse multiply-adds into FMA on arm64
// but not on amd64), so the exact comparison runs where the session was recorded.
func TestReplayReproducesRecordedLiveSession(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("recorded on darwin/arm64; bit-exact float results are architecture-specific")
	}
	b, err := os.ReadFile("../../research/results/live_10m.json")
	if err != nil {
		t.Fatal(err)
	}
	var live struct {
		Hash   string `json:"state_hash"`
		Events uint64 `json:"events"`
	}
	if err := json.Unmarshal(b, &live); err != nil {
		t.Fatal(err)
	}
	fs := newFlagSet()
	ef := addEngineFlags(fs)
	_ = fs.Parse(nil)
	cfg, err := ef.config()
	if err != nil {
		t.Fatal(err)
	}
	r := engine.NewRunner(engine.New(cfg), false)
	if err := replayInto(context.Background(), "../../testdata/deribit-btc-sample.jsonl.gz", r, 0); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.Hash != live.Hash || s.Stats.Events != live.Events {
		t.Fatalf("replay %s (%d events) != live %s (%d events)", s.Hash, s.Stats.Events, live.Hash, live.Events)
	}
}
