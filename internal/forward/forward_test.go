package forward

import (
	"math"
	"testing"

	"github.com/eshaan2418/riskd/internal/pricing"
)

func TestImpliedRecoversForward(t *testing.T) {
	F, T, vol := 79123.0, 0.1, 0.45
	var pairs []Pair
	for K := 60000.0; K <= 100000; K += 1000 {
		c := pricing.GreeksOne(pricing.Opt{Call: true, F: F, K: K, T: T, Vol: vol}).Price / F
		p := pricing.GreeksOne(pricing.Opt{Call: false, F: F, K: K, T: T, Vol: vol}).Price / F
		// Round to Deribit's 0.0001 BTC tick.
		pairs = append(pairs, Pair{K, math.Round(c*1e4) / 1e4, math.Round(p*1e4) / 1e4})
	}
	f, n, ok := Implied(pairs, 78000, 5)
	if !ok || n != 5 || math.Abs(f-F) > 10 {
		t.Fatalf("F=%g n=%d ok=%v (true %g)", f, n, ok, F)
	}
	if _, _, ok := Implied([]Pair{{K: 1, C: 0, P: 0.1}}, 1, 5); ok {
		t.Fatal("pair without call mark accepted")
	}
}
