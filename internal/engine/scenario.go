package engine

import (
	"math"
	"sync"

	"github.com/eshaan2418/riskd/internal/pricing"
)

// Grid is a full-revaluation scenario grid: PnL (USD) for each (forward shock, vol shock),
// sticky strike (each option keeps its own implied vol plus the shock).
type Grid struct {
	TS        int64       `json:"ts"`
	Spot      []float64   `json:"spot_shocks"`
	Vol       []float64   `json:"vol_shocks"`
	PnL       [][]float64 `json:"pnl_usd"` // [spot][vol]
	Worst     float64     `json:"worst_usd"`
	WorstSpot float64     `json:"worst_spot_shock"`
	WorstVol  float64     `json:"worst_vol_shock"`
	Options   int         `json:"options_revalued"`
}

const scenarioChunk = 32 // fixed chunking keeps sums identical for any worker count

type base struct {
	o pricing.Opt
	q float64
	v float64
}

// runScenarios revalues every live position under every grid cell. Work is split into fixed
// chunks of positions; partial grids are summed in chunk order, so the result does not depend
// on how many goroutines ran.
func (e *Engine) runScenarios() {
	ns, nv := len(e.cfg.SpotShocks), len(e.cfg.VolShocks)
	var bs []base
	for i := range e.pos {
		p := &e.pos[i]
		if !p.valued || p.qty == 0 || !(p.t > 0) {
			continue
		}
		in := &e.insts[p.inst].in
		bs = append(bs, base{pricing.Opt{Call: in.Call, F: p.f, K: in.Strike, T: p.t, Vol: p.iv}, p.qty, p.g.Price})
	}
	nch := (len(bs) + scenarioChunk - 1) / scenarioChunk
	parts := make([][]float64, nch)
	work := func(c int) {
		lo, hi := c*scenarioChunk, min((c+1)*scenarioChunk, len(bs))
		m := (hi - lo) * ns * nv
		in := make([]pricing.Opt, 0, m)
		for _, b := range bs[lo:hi] {
			for _, a := range e.cfg.SpotShocks {
				for _, dv := range e.cfg.VolShocks {
					o := b.o
					o.F *= 1 + a
					o.Vol = math.Max(o.Vol+dv, 0.01)
					in = append(in, o)
				}
			}
		}
		out := make([]pricing.Greeks, m)
		e.cfg.Pricer.Greeks(in, out)
		part := make([]float64, ns*nv)
		k := 0
		for _, b := range bs[lo:hi] {
			for c := range part {
				part[c] += b.q * (out[k].Price - b.v)
				k++
			}
		}
		parts[c] = part
	}
	if w := e.cfg.ScenarioWorker; w > 1 && nch > 1 {
		var wg sync.WaitGroup
		next := make(chan int, nch)
		for c := 0; c < nch; c++ {
			next <- c
		}
		close(next)
		for i := 0; i < min(w, nch); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for c := range next {
					work(c)
				}
			}()
		}
		wg.Wait()
	} else {
		for c := 0; c < nch; c++ {
			work(c)
		}
	}
	g := &Grid{TS: e.ts, Spot: e.cfg.SpotShocks, Vol: e.cfg.VolShocks, PnL: make([][]float64, ns), Options: len(bs) * ns * nv}
	for i := range g.PnL {
		g.PnL[i] = make([]float64, nv)
		for j := range g.PnL[i] {
			for _, part := range parts {
				g.PnL[i][j] += part[i*nv+j]
			}
			g.PnL[i][j] += e.spot.qty * e.index * e.cfg.SpotShocks[i]
			if g.PnL[i][j] < g.Worst || (i == 0 && j == 0) {
				g.Worst, g.WorstSpot, g.WorstVol = g.PnL[i][j], e.cfg.SpotShocks[i], e.cfg.VolShocks[j]
			}
		}
	}
	e.grid = g
	e.stats.Scenarios++
}
