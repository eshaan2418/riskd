package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"

	"github.com/eshaan2418/riskd/internal/deribit"
)

// Runner owns an Engine on one goroutine. Readers call Snapshot (an atomic pointer load, no
// locks against the writer) or Subscribe for a stream of snapshots.
type Runner struct {
	e    *Engine
	snap atomic.Pointer[Snapshot]
	proc *hdrhistogram.Histogram // per-event processing time (apply + snapshot), ns
	wire *hdrhistogram.Histogram // receive -> snapshot published, ns (live only)
	live bool

	mu   sync.Mutex
	subs map[chan *Snapshot]struct{}
}

// NewRunner wraps an engine. live=true also records receive-to-publish latency.
func NewRunner(e *Engine, live bool) *Runner {
	r := &Runner{e: e, live: live, subs: map[chan *Snapshot]struct{}{},
		proc: hdrhistogram.New(1, 60_000_000_000, 3), wire: hdrhistogram.New(1, 60_000_000_000, 3)}
	r.snap.Store(e.Snapshot())
	return r
}

// Snapshot returns the latest published snapshot.
func (r *Runner) Snapshot() *Snapshot { return r.snap.Load() }

// Subscribe returns a channel that receives the latest snapshot after each event. Slow readers
// skip intermediate snapshots (buffer of one, newest wins) instead of stalling the engine.
func (r *Runner) Subscribe() (<-chan *Snapshot, func()) {
	ch := make(chan *Snapshot, 1)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

func (r *Runner) publish(s *Snapshot) {
	r.snap.Store(s)
	r.mu.Lock()
	for ch := range r.subs {
		select {
		case ch <- s:
		default:
			select { // drop the stale one, deliver the newest
			case <-ch:
			default:
			}
			select {
			case ch <- s:
			default:
			}
		}
	}
	r.mu.Unlock()
}

func view(h *hdrhistogram.Histogram) *LatencyView {
	if h.TotalCount() == 0 {
		return nil
	}
	us := func(v int64) float64 { return float64(v) / 1e3 }
	return &LatencyView{Count: h.TotalCount(), P50us: us(h.ValueAtQuantile(50)), P99us: us(h.ValueAtQuantile(99)),
		P999us: us(h.ValueAtQuantile(99.9)), MaxUs: us(h.Max())}
}

// Step applies one event and publishes a snapshot.
func (r *Runner) Step(ev *deribit.Event) {
	if ev.Kind == deribit.KindNone {
		return
	}
	t0 := time.Now()
	r.e.Apply(ev)
	s := r.e.Snapshot()
	now := time.Now()
	_ = r.proc.RecordValue(max(1, now.Sub(t0).Nanoseconds()))
	if r.live && ev.RecvNs > 0 {
		_ = r.wire.RecordValue(max(1, now.UnixNano()-ev.RecvNs))
	}
	s.Latency = view(r.proc)
	r.publish(s)
}

// Run consumes events until the channel closes or ctx is done.
func (r *Runner) Run(ctx context.Context, events <-chan deribit.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			r.Step(&ev)
		}
	}
}

// Latencies returns processing and (live) receive-to-publish latency summaries. Call only after
// Run has returned, or from the engine goroutine.
func (r *Runner) Latencies() (proc, wire *LatencyView) { return view(r.proc), view(r.wire) }

// Engine exposes the wrapped engine (single-goroutine use only).
func (r *Runner) Engine() *Engine { return r.e }
