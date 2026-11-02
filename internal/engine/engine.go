// Package engine is the single-writer risk engine: it owns all state, applies market-data and
// trade events in order, maintains position Greeks and PnL incrementally, and publishes
// immutable snapshots that any number of readers can load without locks.
//
// Determinism: the engine runs on exchange timestamps and processes events strictly in journal
// order, and the demo book's trades are drawn from a seeded RNG driven by event time. A replay of
// a recorded session therefore reproduces the live run's state bit for bit (see StateHash).
package engine

import (
	"math"
	"sort"

	"github.com/eshaan2418/riskd/internal/deribit"
	"github.com/eshaan2418/riskd/internal/forward"
	"github.com/eshaan2418/riskd/internal/instr"
	"github.com/eshaan2418/riskd/internal/pricing"
)

// Agg is a sum of qty-weighted per-unit Black-76 quantities (USD, forward-based Greeks).
type Agg struct {
	Value, Delta, Gamma, Vega, Theta, Vanna, Volga float64
}

func (a *Agg) add(q float64, g *pricing.Greeks) {
	a.Value += q * g.Price
	a.Delta += q * g.Delta
	a.Gamma += q * g.Gamma
	a.Vega += q * g.Vega
	a.Theta += q * g.Theta
	a.Vanna += q * g.Vanna
	a.Volga += q * g.Volga
}

// Explain attributes PnL (USD) to the Taylor terms of each revaluation step, plus the PnL of
// trading away from the mark. Sum of all fields == total PnL (tested).
type Explain struct {
	Delta, Gamma, Vega, Theta, Vanna, Volga, Unexplained, NewTrade float64
}

// Sum returns the total of all components.
func (e Explain) Sum() float64 {
	return e.Delta + e.Gamma + e.Vega + e.Theta + e.Vanna + e.Volga + e.Unexplained + e.NewTrade
}

type inst struct {
	in      instr.Instrument
	exp     int32
	mark    float64 // BTC
	dIV     float64 // Deribit IV (fraction)
	hasMark bool
	pos     int32 // index into positions, or -1
}

type expiry struct {
	key     string
	expMs   int64
	insts   []int32
	f       float64 // parity forward (USD)
	nParity int
	fOK     bool
	deribF  float64 // Deribit's underlying_price from the REST summary (validation only)
	agg     Agg
	pos     []int32
}

type position struct {
	inst     int32
	qty      float64
	avgCost  float64 // USD per unit
	realised float64
	valued   bool
	f, t, iv float64
	g        pricing.Greeks
	fallback bool // IV from Deribit's mark_iv because our inversion failed
}

type spot struct {
	qty, avgCost, realised, last float64
}

// Config configures an engine.
type Config struct {
	Pricer         pricing.Pricer
	Limits         Limits
	ScenarioEvery  int64     // event-time ms between scenario grid runs (0: every event)
	SpotShocks     []float64 // relative forward shocks, e.g. -0.2 .. 0.2
	VolShocks      []float64 // absolute vol shocks, e.g. -0.1 .. 0.1
	ScenarioWorker int       // goroutines for the scenario grid (<=1: inline)
	Strategy       Strategy
	KeepPositions  bool // include per-position rows in snapshots
	// VolSource selects each option's vol: VolFromMark (default) inverts the mark price with the
	// pricer; VolFromDeribit uses Deribit's published mark IV and values the option at it.
	VolSource VolSource
}

// VolSource selects how option vols are obtained.
type VolSource uint8

const (
	VolFromMark    VolSource = iota // invert mark x forward (tick-rounded price)
	VolFromDeribit                  // Deribit's own mark IV (unrounded to 1e-4)
)

// Stats are engine counters.
type Stats struct {
	Events, MarkRows, IndexTicks, Revalued, IVFallback, IVFail, Unknown, Trades, SpotTrades, Scenarios uint64
}

// Engine is not safe for concurrent use; see Runner for the goroutine wrapper.
type Engine struct {
	cfg     Config
	insts   []inst
	byName  map[string]int32
	exps    []expiry
	byExp   map[string]int32
	pos     []position
	spot    spot
	index   float64
	ts      int64
	explain Explain
	stats   Stats
	limits  limitState
	grid    *Grid
	lastGr  int64
	alerts  []Alert
	seq     uint64

	dirtyExp []bool
	dirtyPos []bool
	work     []int32
	sel      []int32
	ivIn     []pricing.IVIn
	ivOut    []float64
	ivOK     []bool
	opts     []pricing.Opt
	gOut     []pricing.Greeks
}

// New creates an engine.
func New(cfg Config) *Engine {
	if cfg.Pricer == nil {
		cfg.Pricer = pricing.Default()
	}
	if cfg.SpotShocks == nil {
		cfg.SpotShocks = []float64{-0.2, -0.1, -0.05, -0.02, 0, 0.02, 0.05, 0.1, 0.2}
	}
	if cfg.VolShocks == nil {
		cfg.VolShocks = []float64{-0.1, -0.05, 0, 0.05, 0.1}
	}
	return &Engine{cfg: cfg, byName: map[string]int32{}, byExp: map[string]int32{}}
}

func (e *Engine) instrument(name string) (int32, bool) {
	if i, ok := e.byName[name]; ok {
		return i, true
	}
	in, err := instr.Parse(name)
	if err != nil {
		e.stats.Unknown++
		return -1, false
	}
	x, ok := e.byExp[in.ExpKey]
	if !ok {
		x = int32(len(e.exps))
		e.exps = append(e.exps, expiry{key: in.ExpKey, expMs: in.Expiry.UnixMilli()})
		e.byExp[in.ExpKey] = x
		e.dirtyExp = append(e.dirtyExp, false)
	}
	i := int32(len(e.insts))
	e.insts = append(e.insts, inst{in: in, exp: x, pos: -1})
	e.exps[x].insts = append(e.exps[x].insts, i)
	e.byName[name] = i
	return i, true
}

// Apply processes one decoded event.
func (e *Engine) Apply(ev *deribit.Event) {
	if ev.Kind == deribit.KindNone {
		return
	}
	e.stats.Events++
	if ev.TS > e.ts {
		e.ts = ev.TS
	}
	switch ev.Kind {
	case deribit.KindSummary:
		for _, r := range ev.Summary {
			if i, ok := e.instrument(r.Name); ok {
				e.setMark(i, r.Mark, r.MarkIV)
				e.exps[e.insts[i].exp].deribF = r.Underlying
			}
		}
		e.stats.MarkRows += uint64(len(ev.Summary))
	case deribit.KindMarks:
		for _, m := range ev.Marks {
			if i, ok := e.instrument(m.Name); ok {
				e.setMark(i, m.Mark, m.IV)
			}
		}
		e.stats.MarkRows += uint64(len(ev.Marks))
	case deribit.KindIndex:
		e.stats.IndexTicks++
		if e.spot.qty != 0 && e.index > 0 {
			e.explain.Delta += e.spot.qty * (ev.Index - e.index)
		}
		e.index = ev.Index
		e.spot.last = ev.Index
	}
	e.refresh()
	if e.cfg.Strategy != nil {
		e.cfg.Strategy.OnEvent(e)
	}
	if e.cfg.ScenarioEvery == 0 || e.ts-e.lastGr >= e.cfg.ScenarioEvery {
		e.runScenarios()
		e.lastGr = e.ts
	}
	e.checkLimits()
}

func (e *Engine) setMark(i int32, mark, iv float64) {
	in := &e.insts[i]
	in.mark, in.dIV, in.hasMark = mark, iv, true
	e.dirtyExp[in.exp] = true
	if in.pos >= 0 {
		e.markPosDirty(in.pos)
	}
}

func (e *Engine) markPosDirty(p int32) {
	if !e.dirtyPos[p] {
		e.dirtyPos[p] = true
		e.work = append(e.work, p)
	}
}

// refresh recomputes dirty forwards and revalues dirty positions in one pricer batch.
func (e *Engine) refresh() {
	for x := range e.exps {
		if !e.dirtyExp[x] {
			continue
		}
		e.dirtyExp[x] = false
		ex := &e.exps[x]
		if e.recomputeForward(ex) {
			for _, p := range ex.pos {
				e.markPosDirty(p)
			}
		}
	}
	e.revalue(e.work)
	for _, p := range e.work {
		e.dirtyPos[p] = false
	}
	e.work = e.work[:0]
}

func (e *Engine) recomputeForward(ex *expiry) bool {
	byK := map[float64]*forward.Pair{}
	for _, i := range ex.insts {
		in := &e.insts[i]
		if !in.hasMark {
			continue
		}
		p := byK[in.in.Strike]
		if p == nil {
			p = &forward.Pair{K: in.in.Strike}
			byK[in.in.Strike] = p
		}
		if in.in.Call {
			p.C = in.mark
		} else {
			p.P = in.mark
		}
	}
	pairs := make([]forward.Pair, 0, len(byK))
	for _, p := range byK {
		pairs = append(pairs, *p)
	}
	// Deterministic order for the median's tie-breaking.
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].K < pairs[b].K })
	ref := ex.f
	if !(ref > 0) {
		ref = e.index
	}
	if !(ref > 0) {
		ref = ex.deribF
	}
	if !(ref > 0) && len(pairs) > 0 {
		ref = pairs[len(pairs)/2].K
	}
	f, n, ok := forward.Implied(pairs, ref, 5)
	if !ok {
		return false
	}
	changed := f != ex.f
	ex.f, ex.nParity, ex.fOK = f, n, true
	return changed
}

// revalue reprices positions: IV from the mark (USD price = mark x forward), then Greeks, then
// PnL explain against the previous valuation and an incremental update of the aggregates.
func (e *Engine) revalue(which []int32) {
	e.ivIn, e.opts = e.ivIn[:0], e.opts[:0]
	sel := e.sel[:0]
	for _, p := range which {
		in := &e.insts[e.pos[p].inst]
		ex := &e.exps[in.exp]
		if !ex.fOK || !in.hasMark {
			continue
		}
		t := float64(ex.expMs-e.ts) / instr.MsPerYear
		if !(t > 0) {
			continue // expired: frozen at last valuation (no settlement in this demo)
		}
		sel = append(sel, p)
		e.ivIn = append(e.ivIn, pricing.IVIn{Call: in.in.Call, Price: in.mark * ex.f, F: ex.f, K: in.in.Strike, T: t})
	}
	e.sel = sel
	n := len(sel)
	if n == 0 {
		return
	}
	e.ivOut, e.ivOK = grow(e.ivOut, n), growB(e.ivOK, n)
	if e.cfg.VolSource == VolFromDeribit {
		for k, p := range sel {
			v := e.insts[e.pos[p].inst].dIV
			e.ivOut[k], e.ivOK[k] = v, v > 0
		}
	} else {
		e.cfg.Pricer.IV(e.ivIn, e.ivOut, e.ivOK)
	}
	for k, p := range sel {
		in := &e.insts[e.pos[p].inst]
		vol, fb := e.ivOut[k], false
		if !e.ivOK[k] {
			// A tick-rounded mark at or below intrinsic (deep ITM, short-dated) has no vol
			// information: fall back to Deribit's own mark IV.
			if !(in.dIV > 0) {
				e.stats.IVFail++
				vol = math.NaN()
			} else {
				vol, fb = in.dIV, true
				e.stats.IVFallback++
			}
		}
		q := e.ivIn[k]
		e.opts = append(e.opts, pricing.Opt{Call: q.Call, F: q.F, K: q.K, T: q.T, Vol: vol})
		e.pos[p].fallback = fb
	}
	e.gOut = grow3(e.gOut, n)
	e.cfg.Pricer.Greeks(e.opts, e.gOut)
	for k, p := range sel {
		o := &e.opts[k]
		if math.IsNaN(o.Vol) {
			continue
		}
		e.apply(p, o, &e.gOut[k])
	}
	e.stats.Revalued += uint64(n)
}

func (e *Engine) apply(p int32, o *pricing.Opt, g *pricing.Greeks) {
	ps := &e.pos[p]
	ex := &e.exps[e.insts[ps.inst].exp]
	if ps.valued {
		dF, dv, dt := o.F-ps.f, o.Vol-ps.iv, ps.t-o.T
		og := &ps.g
		q := ps.qty
		d := og.Delta * dF
		gm := 0.5 * og.Gamma * dF * dF
		v := og.Vega * dv
		th := og.Theta * dt
		va := og.Vanna * dF * dv
		vo := 0.5 * og.Volga * dv * dv
		e.explain.Delta += q * d
		e.explain.Gamma += q * gm
		e.explain.Vega += q * v
		e.explain.Theta += q * th
		e.explain.Vanna += q * va
		e.explain.Volga += q * vo
		e.explain.Unexplained += q * ((g.Price - og.Price) - (d + gm + v + th + va + vo))
		ex.agg.add(-q, og)
	}
	ex.agg.add(ps.qty, g)
	ps.valued, ps.f, ps.t, ps.iv, ps.g = true, o.F, o.T, o.Vol, *g
}

// Ready reports whether forwards exist and the index is known.
func (e *Engine) Ready() bool {
	if !(e.index > 0) {
		return false
	}
	for i := range e.exps {
		if e.exps[i].fOK {
			return true
		}
	}
	return false
}

// TradeOption books a trade of qty units (BTC notional; negative = sell) at priceBTC and values
// the position immediately. Returns false if the instrument cannot be valued yet.
func (e *Engine) TradeOption(i int32, qty, priceBTC float64) bool {
	in := &e.insts[i]
	ex := &e.exps[in.exp]
	if !ex.fOK || !in.hasMark || qty == 0 {
		return false
	}
	if in.pos < 0 {
		in.pos = int32(len(e.pos))
		e.pos = append(e.pos, position{inst: i})
		e.dirtyPos = append(e.dirtyPos, false)
		ex.pos = append(ex.pos, in.pos)
		e.revalue([]int32{in.pos})
		if !e.pos[in.pos].valued {
			return false
		}
	}
	ps := &e.pos[in.pos]
	px := priceBTC * ex.f // USD
	e.explain.NewTrade += qty * (ps.g.Price - px)
	ps.realised += book(&ps.qty, &ps.avgCost, qty, px)
	ex.agg.add(qty, &ps.g)
	e.stats.Trades++
	return true
}

// TradeSpot books a spot (index) hedge trade at price USD.
func (e *Engine) TradeSpot(qty, price float64) {
	e.explain.NewTrade += qty * (e.index - price)
	e.spot.realised += book(&e.spot.qty, &e.spot.avgCost, qty, price)
	e.stats.SpotTrades++
}

// book applies a fill to (qty, avgCost) with average-cost accounting and returns realised PnL.
func book(q, avg *float64, dq, px float64) float64 {
	old := *q
	nq := old + dq
	realised := 0.0
	switch {
	case old == 0 || (old > 0) == (dq > 0):
		*avg = (*avg*old + px*dq) / nq
	default:
		closed := math.Min(math.Abs(dq), math.Abs(old))
		sign := 1.0
		if old < 0 {
			sign = -1
		}
		realised = closed * sign * (px - *avg)
		if math.Abs(dq) > math.Abs(old) {
			*avg = px // flipped: the remainder opens at the fill price
		} else if nq == 0 {
			*avg = 0
		}
	}
	*q = nq
	return realised
}

// Totals returns book-level risk: delta in BTC and gamma per 1% move are taken with respect to
// the index, assuming forwards move proportionally (dF/dI = F/I).
func (e *Engine) Totals() Totals {
	var t Totals
	I := e.index
	for i := range e.exps {
		ex := &e.exps[i]
		a := &ex.agg
		t.ValueUSD += a.Value
		if I > 0 && ex.fOK {
			r := ex.f / I
			t.DeltaBTC += a.Delta * r
			t.Gamma1PctBTC += a.Gamma * r * r * 0.01 * I
		}
		t.VegaUSD += a.Vega / 100
		t.ThetaUSDDay += a.Theta / 365
		t.VannaUSD += a.Vanna / 100
		t.VolgaUSD += a.Volga / 1e4
	}
	t.DeltaBTC += e.spot.qty
	t.ValueUSD += e.spot.qty * I
	return t
}

// PnL returns realised, unrealised and total PnL (USD).
func (e *Engine) PnL() (realised, unrealised float64) {
	for i := range e.pos {
		p := &e.pos[i]
		realised += p.realised
		unrealised += p.qty * (p.g.Price - p.avgCost)
	}
	realised += e.spot.realised
	unrealised += e.spot.qty * (e.index - e.spot.avgCost)
	return
}

func grow(s []float64, n int) []float64 {
	if cap(s) < n {
		return make([]float64, n)
	}
	return s[:n]
}

func growB(s []bool, n int) []bool {
	if cap(s) < n {
		return make([]bool, n)
	}
	return s[:n]
}

func grow3(s []pricing.Greeks, n int) []pricing.Greeks {
	if cap(s) < n {
		return make([]pricing.Greeks, n)
	}
	return s[:n]
}

// NumInstruments is the number of instruments seen so far.
func (e *Engine) NumInstruments() int { return len(e.insts) }

// RunScenarios recomputes the scenario grid now (benchmarks).
func (e *Engine) RunScenarios() { e.runScenarios() }

// SetWorkers changes the scenario worker count (benchmarks).
func (e *Engine) SetWorkers(n int) { e.cfg.ScenarioWorker = n }

// Quote returns what the engine currently knows about an instrument: parity forward, time to
// expiry (years), strike and type. ok is false for unknown instruments or expiries without a
// forward.
func (e *Engine) Quote(name string) (f, t, k float64, call, ok bool) {
	i, found := e.byName[name]
	if !found {
		return
	}
	in := &e.insts[i]
	ex := &e.exps[in.exp]
	if !ex.fOK {
		return
	}
	return ex.f, float64(ex.expMs-e.ts) / instr.MsPerYear, in.in.Strike, in.in.Call, true
}
