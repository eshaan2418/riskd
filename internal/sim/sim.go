// Package sim generates synthetic Deribit-shaped event streams (option marks and index ticks)
// from a known model, for tests and for stress benchmarks beyond the size of a real chain.
package sim

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/eshaan2418/riskd/internal/deribit"
	"github.com/eshaan2418/riskd/internal/pricing"
)

// Market is a synthetic option market: one forward (all expiries share the index and carry
// zero basis), a flat-plus-skew smile per expiry, and lognormal forward moves.
type Market struct {
	rng      *rand.Rand
	T0       time.Time
	ts       int64
	F        float64
	VolLevel float64
	Expiries []time.Time
	Strikes  [][]float64
	Tick     float64 // mark rounding in BTC (0 = exact)
	SpotVol  float64 // annualised vol of the forward path
	VolOfVol float64 // annualised vol of the vol level
}

// NewMarket builds a market with nExp expiries (1 day to ~1 year) and nK strikes each.
func NewMarket(seed uint64, nExp, nK int) *Market {
	m := &Market{rng: rand.New(rand.NewPCG(seed, 7)), F: 78000, VolLevel: 0.45, SpotVol: 0.45, VolOfVol: 0.8}
	m.T0 = time.Date(2026, 11, 2, 8, 10, 0, 0, time.UTC)
	m.ts = m.T0.UnixMilli()
	for i := 0; i < nExp; i++ {
		days := 1 + int(math.Round(math.Pow(365, float64(i)/math.Max(1, float64(nExp-1)))))
		d := m.T0.AddDate(0, 0, days)
		m.Expiries = append(m.Expiries, time.Date(d.Year(), d.Month(), d.Day(), 8, 0, 0, 0, time.UTC))
		var ks []float64
		for j := 0; j < nK; j++ {
			x := -0.5 + float64(j)/math.Max(1, float64(nK-1))
			ks = append(ks, math.Round(m.F*math.Exp(x)/100)*100)
		}
		m.Strikes = append(m.Strikes, ks)
	}
	return m
}

// Vol is the model smile: level plus a put skew and curvature.
func (m *Market) Vol(F, K float64) float64 {
	x := math.Log(K / F)
	return math.Max(0.05, m.VolLevel-0.1*x+0.3*x*x)
}

// Name is the Deribit instrument name.
func Name(exp time.Time, K float64, call bool) string {
	t := "P"
	if call {
		t = "C"
	}
	return fmt.Sprintf("BTC-%d%s%s-%d-%s", exp.Day(), upper(exp.Format("Jan")), exp.Format("06"), int64(K), t)
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

// Step advances the market by dtMs and returns the events: an index tick, then the marks of a
// random fraction of instruments (frac = 1: all).
func (m *Market) Step(dtMs int64, frac float64) []deribit.Event {
	dt := float64(dtMs) / (365 * 24 * 3600 * 1000.0)
	m.ts += dtMs
	m.F *= math.Exp(m.SpotVol*math.Sqrt(dt)*m.rng.NormFloat64() - 0.5*m.SpotVol*m.SpotVol*dt)
	m.VolLevel = math.Max(0.15, m.VolLevel*math.Exp(m.VolOfVol*math.Sqrt(dt)*m.rng.NormFloat64()))
	evs := []deribit.Event{{Kind: deribit.KindIndex, TS: m.ts, Index: m.F}}
	ev := deribit.Event{Kind: deribit.KindMarks, TS: m.ts}
	for i, exp := range m.Expiries {
		T := float64(exp.UnixMilli()-m.ts) / (365 * 24 * 3600 * 1000.0)
		if T <= 0 {
			continue
		}
		for _, K := range m.Strikes[i] {
			if frac < 1 && m.rng.Float64() > frac {
				continue
			}
			v := m.Vol(m.F, K)
			for _, call := range []bool{true, false} {
				px := pricing.GreeksOne(pricing.Opt{Call: call, F: m.F, K: K, T: T, Vol: v}).Price / m.F
				if m.Tick > 0 {
					px = math.Max(m.Tick, math.Round(px/m.Tick)*m.Tick)
				}
				ev.Marks = append(ev.Marks, deribit.Mark{Name: Name(exp, K, call), Mark: px, IV: v, TS: m.ts})
			}
		}
	}
	return append(evs, ev)
}

// TS is the current market time (Unix ms).
func (m *Market) TS() int64 { return m.ts }

// Frame encodes an event as the Deribit WebSocket frame that would carry it.
func Frame(ev deribit.Event) []byte {
	switch ev.Kind {
	case deribit.KindIndex:
		return fmt.Appendf(nil, `{"jsonrpc":"2.0","method":"subscription","params":{"channel":"deribit_price_index.btc_usd","data":{"timestamp":%d,"price":%v,"index_name":"btc_usd"}}}`, ev.TS, ev.Index)
	case deribit.KindMarks:
		b := []byte(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"markprice.options.btc_usd","data":[`)
		for i, m := range ev.Marks {
			if i > 0 {
				b = append(b, ',')
			}
			b = fmt.Appendf(b, `{"timestamp":%d,"iv":%v,"instrument_name":%q,"mark_price":%v}`, m.TS, m.IV, m.Name, m.Mark)
		}
		return append(b, "]}}"...)
	}
	return nil
}

// Summary encodes the current full chain as a REST book-summary response.
func (m *Market) Summary() []byte {
	b := []byte(`{"jsonrpc":"2.0","result":[`)
	first := true
	for _, ev := range m.Step(0, 1) {
		for _, r := range ev.Marks {
			if !first {
				b = append(b, ',')
			}
			first = false
			b = fmt.Appendf(b, `{"instrument_name":%q,"mark_price":%v,"mark_iv":%v,"underlying_price":%v,"creation_timestamp":%d}`, r.Name, r.Mark, 100*r.IV, m.F, r.TS)
		}
	}
	return append(b, "]}"...)
}
