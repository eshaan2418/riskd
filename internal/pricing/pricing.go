// Package pricing provides Black-76 prices, Greeks and implied vols for the risk engine through
// a batch interface with two implementations: pure Go (default) and volsurf via cgo
// (-tags volsurf). The batch shape amortises the cgo call overhead.
package pricing

import "math"

// Opt is one Black-76 valuation input (undiscounted: Deribit premiums are paid up front and the
// engine values in forward terms). Layout must match vs_opt in volsurf_shim.cpp.
type Opt struct {
	Call bool
	F    float64
	K    float64
	T    float64 // years
	Vol  float64
}

// Greeks are per one unit of underlying (1 BTC notional), in USD. Vega, vanna and volga are per
// unit of vol (1.00 = 100 vol points); theta is dV/dt per year (negative for a long option).
// Layout must match vs_greeks in volsurf_shim.cpp.
type Greeks struct {
	Price, Delta, Gamma, Vega, Theta, Vanna, Volga float64
}

// IVIn is an implied-vol query: USD option price under Black-76.
type IVIn struct {
	Call  bool
	Price float64
	F     float64
	K     float64
	T     float64
}

// Pricer prices batches. out/vol/ok must have len(in).
type Pricer interface {
	Name() string
	Greeks(in []Opt, out []Greeks)
	IV(in []IVIn, vol []float64, ok []bool)
}

// Go is the pure-Go pricer: textbook d1/d2 Greeks and a safeguarded Newton implied vol.
type Go struct{}

func (Go) Name() string { return "go" }

func ncdf(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }
func npdf(x float64) float64 { return math.Exp(-0.5*x*x) / math.Sqrt(2*math.Pi) }

// GreeksOne computes Black-76 price and Greeks for one option.
func GreeksOne(o Opt) Greeks {
	if !(o.T > 0) || !(o.Vol > 0) {
		in := math.Max(o.F-o.K, 0)
		d := 0.0
		if o.F > o.K {
			d = 1
		}
		if !o.Call {
			in, d = math.Max(o.K-o.F, 0), d-1
		}
		return Greeks{Price: in, Delta: d}
	}
	sq := math.Sqrt(o.T)
	s := o.Vol * sq
	d1 := (math.Log(o.F/o.K) + 0.5*s*s) / s
	d2 := d1 - s
	pd1 := npdf(d1)
	var g Greeks
	if o.Call {
		g.Price = o.F*ncdf(d1) - o.K*ncdf(d2)
		g.Delta = ncdf(d1)
	} else {
		g.Price = o.K*ncdf(-d2) - o.F*ncdf(-d1)
		g.Delta = ncdf(d1) - 1
	}
	g.Gamma = pd1 / (o.F * s)
	g.Vega = o.F * pd1 * sq
	g.Theta = -o.F * pd1 * o.Vol / (2 * sq)
	g.Vanna = -pd1 * d2 / o.Vol
	g.Volga = g.Vega * d1 * d2 / o.Vol
	return g
}

func (Go) Greeks(in []Opt, out []Greeks) {
	for i := range in {
		out[i] = GreeksOne(in[i])
	}
}

// IVOne inverts Black-76 by Newton from the Manaster-Koehler start sqrt(2|ln F/K|/T) (the
// inflection point, from which Newton on the price converges monotonically), with a bisection
// safeguard. Returns ok=false outside the no-arbitrage bounds or without convergence in 100
// iterations; unlike volsurf it does not transform the objective in the wings.
func IVOne(q IVIn) (float64, int, bool) {
	if !(q.T > 0) || !(q.F > 0) || !(q.K > 0) {
		return math.NaN(), 0, false
	}
	intr := math.Max(q.F-q.K, 0)
	upper := q.F
	if !q.Call {
		intr, upper = math.Max(q.K-q.F, 0), q.K
	}
	if !(q.Price > intr) || !(q.Price < upper) {
		return math.NaN(), 0, false
	}
	lo, hi := 1e-8, 50.0
	v := math.Sqrt(2 * math.Abs(math.Log(q.F/q.K)) / q.T)
	if !(v > 0.01) || v > 5 {
		v = 0.5
	}
	o := Opt{Call: q.Call, F: q.F, K: q.K, T: q.T}
	for it := 1; it <= 100; it++ {
		o.Vol = v
		g := GreeksOne(o)
		f := g.Price - q.Price
		if f < 0 {
			lo = v
		} else {
			hi = v
		}
		nv := v - f/g.Vega
		if !(nv > lo && nv < hi) {
			nv = 0.5 * (lo + hi)
		}
		if math.Abs(nv-v) <= 1e-13*v || hi-lo <= 1e-15*hi {
			return nv, it, true
		}
		v = nv
	}
	// Not converged (deep wings: Newton on the raw price crawls when vega/price is tiny).
	return v, 100, false
}

func (Go) IV(in []IVIn, vol []float64, ok []bool) {
	for i := range in {
		vol[i], _, ok[i] = IVOne(in[i])
	}
}
