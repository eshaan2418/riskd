package engine

import (
	"math"
	"math/rand/v2"
)

// Strategy is called after every event and may book trades. It must be deterministic in the
// event stream (no wall clock, seeded randomness) for replays to reproduce live runs.
type Strategy interface {
	OnEvent(e *Engine)
}

// DemoBook builds and trades a synthetic option book so the risk engine has something to risk.
// It is a simulator: no orders are sent anywhere. Fills happen at Deribit's mark plus or minus
// an assumed half-spread, and a delta hedger trades spot at the index plus 1 bp when the book's
// delta leaves the band.
type DemoBook struct {
	Seed             uint64
	Positions        int     // options to open at start
	TradeEveryMs     int64   // event time between random trades
	HedgeBandBTC     float64 // re-hedge when |delta| exceeds this (0: no hedging)
	HalfSpreadFrac   float64 // half-spread as a fraction of the mark
	MinHalfSpreadBTC float64

	rng       *rand.Rand
	opened    bool
	nextTrade int64
}

func (d *DemoBook) eligible(e *Engine) []int32 {
	var out []int32
	for i := range e.insts {
		in := &e.insts[i]
		ex := &e.exps[in.exp]
		if !in.hasMark || !ex.fOK || in.mark < 0.001 {
			continue
		}
		t := float64(ex.expMs-e.ts) / (365 * 24 * 3600 * 1000.0)
		if t < 1.0/365 || math.Abs(math.Log(in.in.Strike/ex.f)) > 0.5 {
			continue
		}
		out = append(out, int32(i))
	}
	return out
}

func (d *DemoBook) fill(e *Engine, i int32, qty float64) {
	m := e.insts[i].mark
	hs := math.Max(d.MinHalfSpreadBTC, d.HalfSpreadFrac*m)
	if qty > 0 {
		m += hs
	} else {
		m -= hs
	}
	e.TradeOption(i, qty, m)
}

func (d *DemoBook) qty() float64 {
	q := float64(1+d.rng.IntN(50)) / 10 // 0.1 .. 5 BTC notional
	if d.rng.IntN(2) == 0 {
		q = -q
	}
	return q
}

func (d *DemoBook) OnEvent(e *Engine) {
	if !e.Ready() {
		return
	}
	if !d.opened {
		d.rng = rand.New(rand.NewPCG(d.Seed, d.Seed^0x9e3779b97f4a7c15))
		el := d.eligible(e)
		for k := 0; k < d.Positions && len(el) > 0; k++ {
			j := d.rng.IntN(len(el))
			d.fill(e, el[j], d.qty())
			el[j] = el[len(el)-1]
			el = el[:len(el)-1]
		}
		d.opened = true
		d.nextTrade = e.ts + d.TradeEveryMs
	}
	if d.TradeEveryMs > 0 && e.ts >= d.nextTrade {
		el := d.eligible(e)
		if len(el) > 0 {
			d.fill(e, el[d.rng.IntN(len(el))], d.qty())
		}
		d.nextTrade += d.TradeEveryMs
	}
	if d.HedgeBandBTC > 0 {
		if delta := e.Totals().DeltaBTC; math.Abs(delta) > d.HedgeBandBTC {
			q := -delta
			px := e.index * (1 + 1e-4*math.Copysign(1, q))
			e.TradeSpot(q, px)
		}
	}
}
