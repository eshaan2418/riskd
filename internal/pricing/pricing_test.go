package pricing

import (
	"math"
	"math/rand/v2"
	"testing"
)

func checkGreeks(t *testing.T, p Pricer) {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 2000; i++ {
		o := Opt{Call: r.IntN(2) == 0, F: 78000, K: 78000 * math.Exp(r.Float64()*1.2-0.6), T: 0.003 + r.Float64(), Vol: 0.2 + r.Float64()*0.8}
		g := one(p, o)
		px := func(m func(*Opt)) float64 { c := o; m(&c); return one(p, c).Price }
		hF, hv, hT := o.F*1e-4, 1e-5, 1e-6
		up, dn := px(func(c *Opt) { c.F += hF }), px(func(c *Opt) { c.F -= hF })
		fdD := (up - dn) / (2 * hF)
		fdG := (up - 2*g.Price + dn) / (hF * hF)
		fdV := (px(func(c *Opt) { c.Vol += hv }) - px(func(c *Opt) { c.Vol -= hv })) / (2 * hv)
		fdT := -(px(func(c *Opt) { c.T += hT }) - px(func(c *Opt) { c.T -= hT })) / (2 * hT)
		dvol := func(f float64) float64 {
			a, b := o, o
			a.F, b.F = o.F+hF, o.F-hF
			a.Vol, b.Vol = o.Vol+f, o.Vol+f
			return (one(p, a).Price - one(p, b).Price) / (2 * hF)
		}
		fdVanna := (dvol(hv) - dvol(-hv)) / (2 * hv)
		vv := func(f float64) float64 { c := o; c.Vol += f; return one(p, c).Price }
		fdVolga := (vv(1e-4) - 2*g.Price + vv(-1e-4)) / 1e-8
		near := func(name string, a, b, tol float64) {
			if math.Abs(a-b) > tol*math.Max(1, math.Abs(b)) {
				t.Fatalf("%s %s: %+v analytic %g fd %g", p.Name(), name, o, a, b)
			}
		}
		near("delta", g.Delta, fdD, 1e-6)
		near("gamma", g.Gamma*o.F, fdG*o.F, 1e-4)
		near("vega", g.Vega, fdV, 1e-5)
		near("theta", g.Theta, fdT, 1e-5)
		near("vanna", g.Vanna, fdVanna, 1e-4)
		// Second difference in vol: rounding noise ~ 4 eps price / h^2 on top of truncation.
		if d := math.Abs(g.Volga - fdVolga); d > 1e-3*math.Max(1, math.Abs(fdVolga))+4*2.2e-16*g.Price/1e-8 {
			t.Fatalf("%s volga: %+v analytic %g fd %g", p.Name(), o, g.Volga, fdVolga)
		}
	}
}

func one(p Pricer, o Opt) Greeks {
	out := make([]Greeks, 1)
	p.Greeks([]Opt{o}, out)
	return out[0]
}

func TestGreeksMatchFiniteDifferences(t *testing.T) {
	for _, p := range Available() {
		checkGreeks(t, p)
	}
}

func TestParityAndExpiry(t *testing.T) {
	for _, p := range Available() {
		c := one(p, Opt{Call: true, F: 100, K: 90, T: 0.5, Vol: 0.4})
		q := one(p, Opt{Call: false, F: 100, K: 90, T: 0.5, Vol: 0.4})
		if math.Abs(c.Price-q.Price-10) > 1e-11 || math.Abs(c.Delta-q.Delta-1) > 1e-14 {
			t.Fatalf("%s parity: %v %v", p.Name(), c, q)
		}
		if g := one(p, Opt{Call: false, F: 100, K: 90, T: 0, Vol: 0.4}); g.Price != 0 || g.Delta != 0 {
			t.Fatalf("%s expired OTM put: %+v", p.Name(), g)
		}
		if g := one(p, Opt{Call: true, F: 100, K: 90, T: -1, Vol: 0.4}); g.Price != 10 || g.Delta != 1 {
			t.Fatalf("%s expired ITM call: %+v", p.Name(), g)
		}
	}
}

// Round trip over the domain a crypto option chain spans (1 day to 2 years, 20%-250% vol,
// log-moneyness +-1.5), skipping prices whose time value is below 1e-9 of the price (no vol
// information at double precision).
func TestIVRoundTrip(t *testing.T) {
	for _, p := range Available() {
		r := rand.New(rand.NewPCG(3, 4))
		var in []IVIn
		var truth []float64
		for len(in) < 20000 {
			o := Opt{Call: r.IntN(2) == 0, F: 78000, T: 1.0/365 + r.Float64()*2, Vol: 0.2 + r.Float64()*2.3}
			o.K = o.F * math.Exp(r.Float64()*3-1.5)
			g := GreeksOne(o)
			intr := math.Max(o.F-o.K, 0)
			if !o.Call {
				intr = math.Max(o.K-o.F, 0)
			}
			if g.Price-intr < 1e-9*g.Price || g.Vega < 1e-6 {
				continue
			}
			in = append(in, IVIn{Call: o.Call, Price: g.Price, F: o.F, K: o.K, T: o.T})
			truth = append(truth, o.Vol)
		}
		vol, ok := make([]float64, len(in)), make([]bool, len(in))
		p.IV(in, vol, ok)
		worst := 0.0
		for i := range in {
			if !ok[i] {
				t.Fatalf("%s: IV failed on %+v", p.Name(), in[i])
			}
			// Conditioning: a price known to ~16 ulps pins vol to 16 eps price / vega.
			lim := 16*2.2e-16*in[i].Price/GreeksOne(Opt{Call: in[i].Call, F: in[i].F, K: in[i].K, T: in[i].T, Vol: truth[i]}).Vega + 1e-12*truth[i]
			worst = math.Max(worst, math.Abs(vol[i]-truth[i])/lim)
		}
		if worst > 1 {
			t.Fatalf("%s: worst error / conditioning limit %.3f", p.Name(), worst)
		}
		t.Logf("%s: %d round trips, worst error / conditioning limit %.3f", p.Name(), len(in), worst)
	}
}

func TestIVRejectsArbitrage(t *testing.T) {
	for _, p := range Available() {
		in := []IVIn{
			{Call: true, Price: 9.99, F: 100, K: 90, T: 1},  // below intrinsic
			{Call: true, Price: 100.5, F: 100, K: 90, T: 1}, // above F
			{Call: false, Price: 90.5, F: 100, K: 90, T: 1}, // above K
			{Call: true, Price: 5, F: 100, K: 90, T: 0},     // expired
		}
		vol, ok := make([]float64, len(in)), make([]bool, len(in))
		p.IV(in, vol, ok)
		for i := range in {
			if ok[i] {
				t.Errorf("%s: accepted %+v -> %g", p.Name(), in[i], vol[i])
			}
		}
	}
}
