package engine

import "math"

// Limits are absolute risk limits; zero disables a limit.
type Limits struct {
	MaxDeltaBTC        float64 `json:"max_delta_btc"`
	MaxGamma1PctBTC    float64 `json:"max_gamma_1pct_btc"`
	MaxVegaBucketUSD   float64 `json:"max_vega_bucket_usd"`
	MaxScenarioLossUSD float64 `json:"max_scenario_loss_usd"`
}

// LimitStatus is a limit's current utilisation.
type LimitStatus struct {
	Name     string  `json:"name"`
	Value    float64 `json:"value"`
	Limit    float64 `json:"limit"`
	Util     float64 `json:"utilisation"`
	Breached bool    `json:"breached"`
}

// Alert is a limit state change. A breach clears only once utilisation falls below ClearAt,
// so a value hovering at the limit does not flap.
type Alert struct {
	TS    int64   `json:"ts"`
	Name  string  `json:"name"`
	Kind  string  `json:"kind"` // "breach" or "clear"
	Value float64 `json:"value"`
	Limit float64 `json:"limit"`
}

// ClearAt is the hysteresis threshold (fraction of the limit).
const ClearAt = 0.9

const maxAlerts = 256

type limitState struct {
	status [4]LimitStatus
}

func (e *Engine) checkLimits() {
	t := e.Totals()
	vb := 0.0
	for i := range e.exps {
		vb = math.Max(vb, math.Abs(e.exps[i].agg.Vega/100))
	}
	loss := 0.0
	if e.grid != nil {
		loss = math.Max(0, -e.grid.Worst)
	}
	vals := [4]struct {
		name  string
		v, lm float64
	}{
		{"delta_btc", math.Abs(t.DeltaBTC), e.cfg.Limits.MaxDeltaBTC},
		{"gamma_1pct_btc", math.Abs(t.Gamma1PctBTC), e.cfg.Limits.MaxGamma1PctBTC},
		{"vega_bucket_usd", vb, e.cfg.Limits.MaxVegaBucketUSD},
		{"scenario_loss_usd", loss, e.cfg.Limits.MaxScenarioLossUSD},
	}
	for i, x := range vals {
		s := &e.limits.status[i]
		s.Name, s.Value, s.Limit = x.name, x.v, x.lm
		if x.lm <= 0 {
			s.Util, s.Breached = 0, false
			continue
		}
		s.Util = x.v / x.lm
		switch {
		case !s.Breached && s.Util > 1:
			s.Breached = true
			e.alert(Alert{e.ts, x.name, "breach", x.v, x.lm})
		case s.Breached && s.Util < ClearAt:
			s.Breached = false
			e.alert(Alert{e.ts, x.name, "clear", x.v, x.lm})
		}
	}
}

func (e *Engine) alert(a Alert) {
	if len(e.alerts) == maxAlerts {
		copy(e.alerts, e.alerts[1:])
		e.alerts = e.alerts[:maxAlerts-1]
	}
	e.alerts = append(e.alerts, a)
}
