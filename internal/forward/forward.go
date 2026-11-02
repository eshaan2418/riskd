// Package forward implies per-expiry forwards from option marks by put-call parity.
//
// Deribit options are inverse: premiums are in BTC. With Black-76 USD prices C, P and forward F,
// parity C - P = F - K becomes, in BTC units c = C/F and p = P/F,
//
//	c - p = 1 - K/F   =>   F = K / (1 - (c - p)).
//
// Marks are rounded to 0.0001 BTC, which moves F by about F^2/K * 1e-4 (~$8 at the money for
// BTC near 78k), so the estimate is the median over the strikes nearest the money.
package forward

import (
	"math"
	"sort"
)

// Pair is a strike with both call and put marks (BTC).
type Pair struct {
	K, C, P float64
}

// Implied returns the median parity forward over the n strikes nearest ref (an initial guess:
// the previous forward or the index) and the number of strikes used. ok is false when no strike
// has both marks.
func Implied(pairs []Pair, ref float64, n int) (f float64, used int, ok bool) {
	type cand struct{ dist, f float64 }
	cs := make([]cand, 0, len(pairs))
	for _, p := range pairs {
		d := 1 - (p.C - p.P)
		if !(p.C > 0) || !(p.P > 0) || !(d > 0) {
			continue
		}
		cs = append(cs, cand{math.Abs(math.Log(p.K / ref)), p.K / d})
	}
	if len(cs) == 0 {
		return 0, 0, false
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].dist < cs[j].dist })
	if len(cs) > n {
		cs = cs[:n]
	}
	fs := make([]float64, len(cs))
	for i, c := range cs {
		fs[i] = c.f
	}
	sort.Float64s(fs)
	m := len(fs) / 2
	if len(fs)%2 == 1 {
		return fs[m], len(fs), true
	}
	return 0.5 * (fs[m-1] + fs[m]), len(fs), true
}
