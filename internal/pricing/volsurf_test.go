//go:build volsurf

package pricing

import (
	"math"
	"math/rand/v2"
	"testing"
)

// Differential: the cgo volsurf backend and the pure-Go backend agree on Greeks and IV.
func TestVolsurfMatchesGo(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	n := 50000
	in := make([]Opt, n)
	for i := range in {
		in[i] = Opt{Call: r.IntN(2) == 0, F: 78000, K: 78000 * math.Exp(r.Float64()*2-1), T: 0.002 + r.Float64()*2, Vol: 0.15 + r.Float64()}
	}
	a, b := make([]Greeks, n), make([]Greeks, n)
	Go{}.Greeks(in, a)
	Volsurf{}.Greeks(in, b)
	rel := func(x, y float64) float64 { return math.Abs(x-y) / math.Max(math.Abs(y), 1e-300) }
	for i := range in {
		x, y := a[i], b[i]
		if math.Abs(x.Price-y.Price) > 1e-9*math.Max(y.Price, 1) || math.Abs(x.Delta-y.Delta) > 1e-12 ||
			rel(x.Gamma, y.Gamma) > 1e-9 || rel(x.Vega, y.Vega) > 1e-9 || rel(x.Theta, y.Theta) > 1e-9 ||
			math.Abs(x.Vanna-y.Vanna) > 1e-9*math.Max(1, math.Abs(y.Vanna)) || math.Abs(x.Volga-y.Volga) > 1e-7*math.Max(1, math.Abs(y.Volga)) {
			t.Fatalf("%+v\n go      %+v\n volsurf %+v", in[i], x, y)
		}
	}
}

// Deep wings: volsurf keeps relative precision where the textbook Go price does not.
func TestVolsurfDeepWing(t *testing.T) {
	// ln K/F = 1, one day, 60% vol: about 32 standard deviations out of the money.
	o := Opt{Call: true, F: 78000, K: 78000 * math.E, T: 1.0 / 365, Vol: 0.6}
	vs := one(Volsurf{}, o).Price
	if !(vs > 0) || vs > 1e-100 {
		t.Fatalf("volsurf deep OTM price %g", vs)
	}
	q := IVIn{Call: true, Price: vs, F: o.F, K: o.K, T: o.T}
	vol, ok := make([]float64, 1), make([]bool, 1)
	Volsurf{}.IV([]IVIn{q}, vol, ok)
	if !ok[0] || math.Abs(vol[0]-0.6) > 1e-9 {
		t.Fatalf("volsurf deep-wing IV %v %g", ok[0], vol[0])
	}
	gv, _, gok := IVOne(q)
	t.Logf("deep OTM price %.3e: volsurf IV %.15f; textbook Go price %.6e (rel diff %.1e), Go IV ok=%v (%.6f)",
		vs, vol[0], GreeksOne(o).Price, math.Abs(GreeksOne(o).Price-vs)/vs, gok, gv)
}
