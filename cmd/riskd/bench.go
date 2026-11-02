package main

import (
	"flag"
	"math"
	"math/rand/v2"
	"runtime"
	"time"

	"github.com/eshaan2418/riskd/internal/engine"
	"github.com/eshaan2418/riskd/internal/pricing"
	"github.com/eshaan2418/riskd/internal/sim"
)

func nowNs() int64 { return time.Now().UnixNano() }

func median(f func() float64, reps int) float64 {
	xs := make([]float64, reps)
	for i := range xs {
		xs[i] = f()
	}
	for i := range xs { // insertion sort; reps is small
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
	return xs[reps/2]
}

func chain(n int) []pricing.Opt {
	r := rand.New(rand.NewPCG(11, 12))
	o := make([]pricing.Opt, n)
	for i := range o {
		o[i] = pricing.Opt{Call: r.IntN(2) == 0, F: 78000, K: 78000 * math.Exp(r.Float64()-0.5), T: 1.0/365 + r.Float64(), Vol: 0.3 + 0.4*r.Float64()}
	}
	return o
}

type pricerBench struct {
	Pricer        string             `json:"pricer"`
	GreeksNsBatch map[string]float64 `json:"greeks_ns_per_option_by_batch"`
	IVNsPerSolve  float64            `json:"iv_ns_per_solve_batch_4096"`
}

type engineBench struct {
	Pricer           string             `json:"pricer"`
	Positions        int                `json:"positions"`
	FullRevalMs      float64            `json:"full_revaluation_ms"`
	NsPerPosition    float64            `json:"full_revaluation_ns_per_position"`
	ScenarioCells    int                `json:"scenario_cells"`
	ScenarioMsByWork map[string]float64 `json:"scenario_grid_ms_by_workers"`
}

func cmdBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	out := fs.String("out", "-", "result JSON path")
	sizes := fs.String("sizes", "1000,10000,100000", "engine book sizes")
	_ = fs.Parse(args)
	var pb []pricerBench
	opts := chain(4096)
	for _, p := range pricing.Available() {
		b := pricerBench{Pricer: p.Name(), GreeksNsBatch: map[string]float64{}}
		for _, bs := range []int{1, 8, 64, 512, 4096} {
			out := make([]pricing.Greeks, bs)
			b.GreeksNsBatch[itoa(bs)] = median(func() float64 {
				t0 := nowNs()
				for off := 0; off+bs <= len(opts); off += bs {
					for k := 0; k < 8; k++ {
						p.Greeks(opts[off:off+bs], out)
					}
				}
				return float64(nowNs()-t0) / float64(8*(len(opts)/bs)*bs)
			}, 7)
		}
		g := make([]pricing.Greeks, len(opts))
		pricing.Go{}.Greeks(opts, g)
		in := make([]pricing.IVIn, len(opts))
		for i, o := range opts {
			in[i] = pricing.IVIn{Call: o.Call, Price: g[i].Price, F: o.F, K: o.K, T: o.T}
		}
		vol, ok := make([]float64, len(in)), make([]bool, len(in))
		b.IVNsPerSolve = median(func() float64 {
			t0 := nowNs()
			p.IV(in, vol, ok)
			return float64(nowNs()-t0) / float64(len(in))
		}, 7)
		pb = append(pb, b)
	}
	var eb []engineBench
	for _, n := range parseInts(*sizes) {
		for _, p := range pricing.Available() {
			eb = append(eb, benchEngine(p, n))
		}
	}
	return writeJSON(*out, map[string]any{
		"gomaxprocs": runtime.GOMAXPROCS(0), "pricers": pb, "engine": eb,
		"note": "synthetic chain (internal/sim); every position is a distinct instrument; full revaluation = one marks event touching every instrument (forward moves, so every position reprices)",
	})
}

func benchEngine(p pricing.Pricer, n int) engineBench {
	nExp := 20
	nK := max(1, n/(2*nExp))
	m := sim.NewMarket(21, nExp, nK)
	e := engine.New(engine.Config{Pricer: p, ScenarioEvery: math.MaxInt64 / 4})
	r := engine.NewRunner(e, false)
	evs := m.Step(0, 1)
	for i := range evs {
		r.Step(&evs[i])
	}
	for i := 0; i < e.NumInstruments(); i++ {
		e.TradeOption(int32(i), 1, 0)
	}
	b := engineBench{Pricer: p.Name(), Positions: e.Snapshot().NumPositions, ScenarioMsByWork: map[string]float64{}}
	b.FullRevalMs = median(func() float64 {
		evs := m.Step(1000, 1)
		t0 := nowNs()
		for i := range evs {
			e.Apply(&evs[i])
		}
		return float64(nowNs()-t0) / 1e6
	}, 5)
	b.NsPerPosition = b.FullRevalMs * 1e6 / float64(b.Positions)
	for _, w := range []int{1, 4, runtime.GOMAXPROCS(0)} {
		e.SetWorkers(w)
		b.ScenarioMsByWork[itoa(w)] = median(func() float64 {
			t0 := nowNs()
			e.RunScenarios()
			return float64(nowNs()-t0) / 1e6
		}, 3)
	}
	b.ScenarioCells = 45
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func parseInts(s string) []int {
	var out []int
	n, have := 0, false
	for _, c := range s + "," {
		if c >= '0' && c <= '9' {
			n, have = n*10+int(c-'0'), true
		} else if have {
			out = append(out, n)
			n, have = 0, false
		}
	}
	return out
}
