package engine

import (
	"encoding/binary"
	"hash/fnv"
	"math"
)

// Totals is book-level risk. Units: delta and gamma in BTC with respect to the index (gamma per
// 1% index move), vega USD per vol point, theta USD per day, vanna BTC of delta per vol point,
// volga USD per vol point squared.
type Totals struct {
	ValueUSD     float64 `json:"value_usd"`
	DeltaBTC     float64 `json:"delta_btc"`
	Gamma1PctBTC float64 `json:"gamma_1pct_btc"`
	VegaUSD      float64 `json:"vega_usd_per_volpt"`
	ThetaUSDDay  float64 `json:"theta_usd_per_day"`
	VannaUSD     float64 `json:"vanna_per_volpt"`
	VolgaUSD     float64 `json:"volga_usd_per_volpt2"`
}

// ExpiryRow is per-expiry risk and the forward diagnostics.
type ExpiryRow struct {
	Key           string  `json:"expiry"`
	T             float64 `json:"t_years"`
	Forward       float64 `json:"parity_forward"`
	DeribitFwd    float64 `json:"deribit_forward_at_start"`
	ParityStrikes int     `json:"parity_strikes"`
	Positions     int     `json:"positions"`
	Delta         float64 `json:"delta_fwd"`
	VegaUSD       float64 `json:"vega_usd_per_volpt"`
	ThetaUSDDay   float64 `json:"theta_usd_per_day"`
}

// PositionRow is one option position.
type PositionRow struct {
	Instrument string  `json:"instrument"`
	Qty        float64 `json:"qty"`
	AvgCostUSD float64 `json:"avg_cost_usd"`
	MarkBTC    float64 `json:"mark_btc"`
	IV         float64 `json:"iv"`
	DeribitIV  float64 `json:"deribit_iv"`
	Fallback   bool    `json:"iv_fallback"`
	ValueUSD   float64 `json:"value_usd"`
	PnLUSD     float64 `json:"pnl_usd"`
	Delta      float64 `json:"delta"`
	VegaUSD    float64 `json:"vega_usd_per_volpt"`
}

// PnLView is PnL with its explain.
type PnLView struct {
	Total      float64 `json:"total_usd"`
	Realised   float64 `json:"realised_usd"`
	Unrealised float64 `json:"unrealised_usd"`
	Explain    Explain `json:"explain"`
}

// Snapshot is an immutable view of the engine; readers may hold it indefinitely.
type Snapshot struct {
	Seq          uint64        `json:"seq"`
	TS           int64         `json:"ts"`
	Index        float64       `json:"index"`
	Pricer       string        `json:"pricer"`
	Totals       Totals        `json:"totals"`
	PnL          PnLView       `json:"pnl"`
	SpotQty      float64       `json:"spot_hedge_btc"`
	NumPositions int           `json:"positions"`
	Expiries     []ExpiryRow   `json:"expiries"`
	Positions    []PositionRow `json:"position_rows,omitempty"`
	Scenarios    *Grid         `json:"scenarios"`
	Limits       []LimitStatus `json:"limits"`
	Alerts       []Alert       `json:"alerts"`
	Stats        Stats         `json:"stats"`
	Hash         string        `json:"state_hash"`
	Latency      *LatencyView  `json:"latency,omitempty"`
}

// LatencyView summarises event processing latency (filled in by Runner).
type LatencyView struct {
	Count  int64   `json:"count"`
	P50us  float64 `json:"p50_us"`
	P99us  float64 `json:"p99_us"`
	P999us float64 `json:"p999_us"`
	MaxUs  float64 `json:"max_us"`
}

// Snapshot builds an immutable snapshot.
func (e *Engine) Snapshot() *Snapshot {
	e.seq++
	r, u := e.PnL()
	s := &Snapshot{
		Seq: e.seq, TS: e.ts, Index: e.index, Pricer: e.cfg.Pricer.Name(), Totals: e.Totals(),
		PnL: PnLView{Total: r + u, Realised: r, Unrealised: u, Explain: e.explain}, SpotQty: e.spot.qty,
		NumPositions: len(e.pos), Scenarios: e.grid, Stats: e.stats, Hash: e.StateHashHex(),
	}
	s.Limits = append([]LimitStatus(nil), e.limits.status[:]...)
	s.Alerts = append([]Alert(nil), e.alerts...)
	for i := range e.exps {
		ex := &e.exps[i]
		if !ex.fOK {
			continue
		}
		s.Expiries = append(s.Expiries, ExpiryRow{
			Key: ex.key, T: float64(ex.expMs-e.ts) / (365 * 24 * 3600 * 1000.0), Forward: ex.f, DeribitFwd: ex.deribF,
			ParityStrikes: ex.nParity, Positions: len(ex.pos), Delta: ex.agg.Delta, VegaUSD: ex.agg.Vega / 100, ThetaUSDDay: ex.agg.Theta / 365,
		})
	}
	if e.cfg.KeepPositions {
		for i := range e.pos {
			p := &e.pos[i]
			in := &e.insts[p.inst]
			s.Positions = append(s.Positions, PositionRow{
				Instrument: in.in.Name, Qty: p.qty, AvgCostUSD: p.avgCost, MarkBTC: in.mark, IV: p.iv, DeribitIV: in.dIV,
				Fallback: p.fallback, ValueUSD: p.qty * p.g.Price, PnLUSD: p.realised + p.qty*(p.g.Price-p.avgCost),
				Delta: p.qty * p.g.Delta, VegaUSD: p.qty * p.g.Vega / 100,
			})
		}
	}
	return s
}

// StateHash is FNV-1a over the exact bits of every position, the hedge, the explain and the
// latest scenario grid: equal hashes mean bit-identical state.
func (e *Engine) StateHash() uint64 {
	h := fnv.New64a()
	var b [8]byte
	f := func(x float64) {
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(x))
		h.Write(b[:])
	}
	for i := range e.pos {
		p := &e.pos[i]
		h.Write([]byte(e.insts[p.inst].in.Name))
		for _, x := range []float64{p.qty, p.avgCost, p.realised, p.f, p.t, p.iv, p.g.Price, p.g.Delta, p.g.Vega} {
			f(x)
		}
	}
	for _, x := range []float64{e.spot.qty, e.spot.avgCost, e.spot.realised, e.index} {
		f(x)
	}
	x := e.explain
	for _, v := range []float64{x.Delta, x.Gamma, x.Vega, x.Theta, x.Vanna, x.Volga, x.Unexplained, x.NewTrade} {
		f(v)
	}
	if e.grid != nil {
		for _, row := range e.grid.PnL {
			for _, v := range row {
				f(v)
			}
		}
	}
	return h.Sum64()
}

// StateHashHex formats StateHash.
func (e *Engine) StateHashHex() string {
	const hexd = "0123456789abcdef"
	v := e.StateHash()
	var out [16]byte
	for i := 15; i >= 0; i-- {
		out[i] = hexd[v&15]
		v >>= 4
	}
	return string(out[:])
}
