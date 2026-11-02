package engine

import (
	"math"
	"testing"

	"github.com/eshaan2418/riskd/internal/pricing"
	"github.com/eshaan2418/riskd/internal/sim"
)

func demo(seed uint64) *DemoBook {
	return &DemoBook{Seed: seed, Positions: 40, TradeEveryMs: 5000, HedgeBandBTC: 2, HalfSpreadFrac: 0.01, MinHalfSpreadBTC: 0.0001}
}

func run(t *testing.T, cfg Config, seed uint64, steps int, dtMs int64, frac float64) *Engine {
	t.Helper()
	m := sim.NewMarket(seed, 6, 15)
	e := New(cfg)
	for _, ev := range m.Step(0, 1) { // full chain first
		e.Apply(&ev)
	}
	for s := 0; s < steps; s++ {
		for _, ev := range m.Step(dtMs, frac) {
			e.Apply(&ev)
		}
	}
	return e
}

// Total PnL (realised + unrealised, average cost) equals the sum of the explain components
// (Taylor terms + unexplained + PnL of trading away from the mark), through opens, random
// trades, position flips and spot hedges.
func TestPnLIdentity(t *testing.T) {
	e := run(t, Config{Strategy: demo(1), Pricer: pricing.Go{}}, 1, 600, 1000, 0.3)
	r, u := e.PnL()
	if e.stats.Trades < 100 || e.stats.SpotTrades == 0 {
		t.Fatalf("not enough activity: %+v", e.stats)
	}
	if d := math.Abs(r + u - e.explain.Sum()); d > 1e-6*math.Max(1, math.Abs(r+u)) {
		t.Fatalf("PnL %.6f != explain sum %.6f (diff %g)", r+u, e.explain.Sum(), d)
	}
}

// Incrementally maintained aggregates equal a from-scratch sum over positions.
func TestIncrementalAggregatesMatchFullRecompute(t *testing.T) {
	e := run(t, Config{Strategy: demo(2), Pricer: pricing.Go{}}, 2, 400, 1000, 0.5)
	for x := range e.exps {
		var full Agg
		for _, p := range e.exps[x].pos {
			full.add(e.pos[p].qty, &e.pos[p].g)
		}
		inc := e.exps[x].agg
		for _, c := range [][2]float64{{inc.Value, full.Value}, {inc.Delta, full.Delta}, {inc.Gamma, full.Gamma}, {inc.Vega, full.Vega}, {inc.Theta, full.Theta}, {inc.Vanna, full.Vanna}, {inc.Volga, full.Volga}} {
			if math.Abs(c[0]-c[1]) > 1e-8*math.Max(1, math.Abs(c[1])) {
				t.Fatalf("expiry %s: incremental %v vs full %v", e.exps[x].key, inc, full)
			}
		}
	}
}

// The second-order explain leaves a third-order remainder: shrinking the step 4x should shrink
// the unexplained PnL per step by far more than 4x, and it must be small relative to the
// explained moves.
func TestExplainIsThirdOrder(t *testing.T) {
	une := func(dt int64, steps int) (float64, float64) {
		b := &DemoBook{Seed: 3, Positions: 40, HalfSpreadFrac: 0.01, MinHalfSpreadBTC: 0.0001}
		e := run(t, Config{Strategy: b, Pricer: pricing.Go{}}, 3, steps, dt, 1)
		x := e.explain
		moved := math.Abs(x.Delta) + math.Abs(x.Gamma) + math.Abs(x.Vega) + math.Abs(x.Theta)
		return math.Abs(x.Unexplained) / float64(steps), moved
	}
	u1, m1 := une(60000, 60)
	u2, _ := une(15000, 240)
	if !(u1 < 1e-3*m1) {
		t.Fatalf("unexplained %g vs moved %g", u1*60, m1)
	}
	if !(u2 < u1/6) {
		t.Fatalf("unexplained per step: dt=60s %g, dt=15s %g (want ratio > 6)", u1, u2)
	}
	t.Logf("unexplained per step: dt=60s %.3e, dt=15s %.3e (ratio %.1f)", u1, u2, u1/u2)
}

// Same events => same state, regardless of the scenario worker count.
func TestDeterministicAcrossWorkers(t *testing.T) {
	h := func(w int) string {
		e := run(t, Config{Strategy: demo(4), Pricer: pricing.Go{}, ScenarioWorker: w, ScenarioEvery: 10000}, 4, 200, 1000, 0.4)
		return e.StateHashHex()
	}
	a, b, c := h(1), h(8), h(1)
	if a != b || a != c {
		t.Fatalf("hashes differ: %s %s %s", a, b, c)
	}
}

func TestParityForwardInEngine(t *testing.T) {
	m := sim.NewMarket(5, 4, 21)
	m.Tick = 0.0001
	e := New(Config{Pricer: pricing.Go{}})
	for _, ev := range m.Step(0, 1) {
		e.Apply(&ev)
	}
	for _, ex := range e.exps {
		if !ex.fOK || math.Abs(ex.f-m.F) > 20 {
			t.Fatalf("%s: parity F %.2f vs true %.2f", ex.key, ex.f, m.F)
		}
	}
}

func TestScenarioGridMatchesBruteForce(t *testing.T) {
	e := run(t, Config{Strategy: &DemoBook{Seed: 6, Positions: 10, HalfSpreadFrac: 0.01, MinHalfSpreadBTC: 0.0001}, Pricer: pricing.Go{}}, 6, 3, 1000, 1)
	e.runScenarios()
	g := e.grid
	for i, a := range g.Spot {
		for j, dv := range g.Vol {
			want := e.spot.qty * e.index * a
			for _, p := range e.pos {
				in := e.insts[p.inst].in
				o := pricing.Opt{Call: in.Call, F: p.f * (1 + a), K: in.Strike, T: p.t, Vol: math.Max(p.iv+dv, 0.01)}
				want += p.qty * (pricing.GreeksOne(o).Price - p.g.Price)
			}
			if math.Abs(g.PnL[i][j]-want) > 1e-6*math.Max(1, math.Abs(want)) {
				t.Fatalf("cell (%g,%g): %g vs %g", a, dv, g.PnL[i][j], want)
			}
			if a == 0 && dv == 0 && math.Abs(g.PnL[i][j]) > 1e-9 {
				t.Fatalf("zero shock PnL %g", g.PnL[i][j])
			}
		}
	}
}

func TestAverageCostBook(t *testing.T) {
	var q, avg float64
	if r := book(&q, &avg, 2, 10); r != 0 || q != 2 || avg != 10 {
		t.Fatal(q, avg, r)
	}
	if r := book(&q, &avg, 2, 20); r != 0 || avg != 15 {
		t.Fatal(q, avg, r)
	}
	if r := book(&q, &avg, -1, 25); r != 10 || q != 3 || avg != 15 {
		t.Fatal(q, avg, r)
	}
	if r := book(&q, &avg, -5, 5); r != -30 || q != -2 || avg != 5 { // flip short at 5
		t.Fatal(q, avg, r)
	}
	if r := book(&q, &avg, 2, 4); r != 2 || q != 0 || avg != 0 {
		t.Fatal(q, avg, r)
	}
}

func TestLimitHysteresis(t *testing.T) {
	e := New(Config{Pricer: pricing.Go{}, Limits: Limits{MaxDeltaBTC: 10}})
	e.index = 1
	for _, d := range []float64{5, 11, 10.5, 9.5, 9.1, 8.9, 9.8, 10.2} {
		e.spot.qty = d
		e.checkLimits()
	}
	kinds := ""
	for _, a := range e.alerts {
		kinds += a.Kind[:1]
	}
	// Breach at 11; 10.5, 9.5 and 9.1 stay breached (hysteresis); clear at 8.9 (< 90%); 9.8 stays
	// clear; 10.2 breaches again.
	if kinds != "bcb" {
		t.Fatalf("alerts %q", kinds)
	}
}
