package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/eshaan2418/riskd/internal/engine"
	"github.com/eshaan2418/riskd/internal/pricing"
)

type engineFlags struct {
	pricer     *string
	positions  *int
	seed       *uint64
	tradeEvery *time.Duration
	hedgeBand  *float64
	scenEvery  *time.Duration
	workers    *int
	maxDelta   *float64
	maxGamma   *float64
	maxVega    *float64
	maxLoss    *float64
	volSource  *string
}

func newFlagSet() *flag.FlagSet { return flag.NewFlagSet("test", flag.ContinueOnError) }

func addEngineFlags(fs *flag.FlagSet) *engineFlags {
	return &engineFlags{
		pricer:     fs.String("pricer", pricing.Default().Name(), "go or volsurf (volsurf needs -tags volsurf)"),
		positions:  fs.Int("positions", 150, "demo book: options opened at start"),
		seed:       fs.Uint64("seed", 1, "demo book RNG seed"),
		tradeEvery: fs.Duration("trade-every", 20*time.Second, "demo book: event time between random trades"),
		hedgeBand:  fs.Float64("hedge-band", 5, "delta hedge band in BTC (0: no hedging)"),
		scenEvery:  fs.Duration("scenario-every", time.Second, "event time between scenario grid runs"),
		workers:    fs.Int("scenario-workers", 4, "goroutines for the scenario grid"),
		maxDelta:   fs.Float64("limit-delta", 4, "limit: |delta| BTC"),
		maxGamma:   fs.Float64("limit-gamma", 15, "limit: |gamma| BTC per 1% move"),
		maxVega:    fs.Float64("limit-vega", 20000, "limit: |vega| USD per vol point, any expiry"),
		maxLoss:    fs.Float64("limit-loss", 2.5e6, "limit: worst scenario loss USD"),
		volSource:  fs.String("vol", "mark", "vol source: mark (invert the tick-rounded mark) or deribit (Deribit mark IV)"),
	}
}

func (f *engineFlags) config() (engine.Config, error) {
	var p pricing.Pricer
	for _, a := range pricing.Available() {
		if a.Name() == *f.pricer {
			p = a
		}
	}
	if p == nil {
		return engine.Config{}, fmt.Errorf("pricer %q not compiled in (build with -tags volsurf for volsurf)", *f.pricer)
	}
	vs := engine.VolFromMark
	switch *f.volSource {
	case "mark":
	case "deribit":
		vs = engine.VolFromDeribit
	default:
		return engine.Config{}, fmt.Errorf("-vol must be mark or deribit")
	}
	return engine.Config{
		VolSource:      vs,
		Pricer:         p,
		ScenarioEvery:  f.scenEvery.Milliseconds(),
		ScenarioWorker: *f.workers,
		KeepPositions:  true,
		Limits:         engine.Limits{MaxDeltaBTC: *f.maxDelta, MaxGamma1PctBTC: *f.maxGamma, MaxVegaBucketUSD: *f.maxVega, MaxScenarioLossUSD: *f.maxLoss},
		Strategy: &engine.DemoBook{Seed: *f.seed, Positions: *f.positions, TradeEveryMs: f.tradeEvery.Milliseconds(),
			HedgeBandBTC: *f.hedgeBand, HalfSpreadFrac: 0.01, MinHalfSpreadBTC: 0.0001},
	}, nil
}
