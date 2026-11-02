package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/eshaan2418/riskd/internal/deribit"
	"github.com/eshaan2418/riskd/internal/engine"
	"github.com/eshaan2418/riskd/internal/instr"
	"github.com/eshaan2418/riskd/internal/pricing"
)

type dist struct {
	N      int     `json:"n"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
}

func mkdist(x []float64) dist {
	if len(x) == 0 {
		return dist{}
	}
	sort.Float64s(x)
	q := func(p float64) float64 { return x[min(len(x)-1, int(p*float64(len(x))))] }
	return dist{len(x), q(0.5), q(0.9), q(0.99), x[len(x)-1]}
}

type ivReport struct {
	Pricer        string  `json:"pricer"`
	Solved        int     `json:"solved"`
	Failed        int     `json:"failed"`
	FailedOTM1d   int     `json:"failed_otm_t_ge_1d"`
	DiffVolPtsOTM dist    `json:"abs_diff_vs_mark_iv_volpts_otm_t_ge_1d"`
	DiffVolPtsAll dist    `json:"abs_diff_vs_mark_iv_volpts_all"`
	NsPerSolve    float64 `json:"ns_per_solve"`
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	out := fs.String("out", "-", "result JSON path")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: riskd report [flags] JOURNAL")
	}
	jr, err := deribit.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer jr.Close()
	var summary *deribit.Event
	var frames, marks, rows, idx int
	var firstTS, lastTS int64
	var gaps []float64
	var prevMarkTS int64
	for {
		l, err := jr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		frames++
		ev, err := deribit.Decode(l)
		if err != nil {
			return err
		}
		switch ev.Kind {
		case deribit.KindSummary:
			e := ev
			summary = &e
		case deribit.KindMarks:
			marks++
			rows += len(ev.Marks)
			if prevMarkTS > 0 {
				gaps = append(gaps, float64(ev.TS-prevMarkTS))
			}
			prevMarkTS = ev.TS
		case deribit.KindIndex:
			idx++
		}
		if ev.TS > 0 {
			if firstTS == 0 {
				firstTS = ev.TS
			}
			lastTS = ev.TS
		}
	}
	if summary == nil {
		return fmt.Errorf("journal has no REST summary line")
	}
	// Parity forwards from the summary marks vs Deribit's own underlying price per expiry.
	e := engine.New(engine.Config{Pricer: pricing.Go{}})
	e.Apply(summary)
	type fwdRow struct {
		Expiry  string  `json:"expiry"`
		T       float64 `json:"t_years"`
		Parity  float64 `json:"parity_forward"`
		Deribit float64 `json:"deribit_underlying"`
		DiffBps float64 `json:"diff_bps"`
		Strikes int     `json:"parity_strikes"`
	}
	var fwd []fwdRow
	var fdiff []float64
	for _, x := range e.Snapshot().Expiries {
		if x.T <= 0 || x.DeribitFwd <= 0 {
			continue
		}
		d := 1e4 * (x.Forward/x.DeribitFwd - 1)
		fwd = append(fwd, fwdRow{x.Key, x.T, x.Forward, x.DeribitFwd, d, x.ParityStrikes})
		fdiff = append(fdiff, math.Abs(d))
	}
	sort.Slice(fwd, func(a, b int) bool { return fwd[a].T < fwd[b].T })
	// Implied vols from the marks (forward = Deribit's underlying, to isolate the solver).
	var ivs []ivReport
	for _, p := range pricing.Available() {
		var in []pricing.IVIn
		var ref []float64
		var otm []bool
		for _, r := range summary.Summary {
			i, err := instr.Parse(r.Name)
			if err != nil {
				continue
			}
			T := i.YearFrac(summary.TS)
			if T <= 0 || !(r.Mark > 0) || !(r.Underlying > 0) {
				continue
			}
			in = append(in, pricing.IVIn{Call: i.Call, Price: r.Mark * r.Underlying, F: r.Underlying, K: i.Strike, T: T})
			ref = append(ref, r.MarkIV)
			otm = append(otm, T >= 1.0/365 && ((i.Call && i.Strike >= r.Underlying) || (!i.Call && i.Strike < r.Underlying)))
		}
		vol, ok := make([]float64, len(in)), make([]bool, len(in))
		const reps = 200
		t0 := nowNs()
		for k := 0; k < reps; k++ {
			p.IV(in, vol, ok)
		}
		ns := float64(nowNs()-t0) / float64(reps*len(in))
		rep := ivReport{Pricer: p.Name(), NsPerSolve: ns}
		var dOTM, dAll []float64
		for k := range in {
			if !ok[k] {
				rep.Failed++
				if otm[k] {
					rep.FailedOTM1d++
				}
				continue
			}
			rep.Solved++
			d := 100 * math.Abs(vol[k]-ref[k])
			dAll = append(dAll, d)
			if otm[k] {
				dOTM = append(dOTM, d)
			}
		}
		rep.DiffVolPtsOTM, rep.DiffVolPtsAll = mkdist(dOTM), mkdist(dAll)
		ivs = append(ivs, rep)
	}
	noise, err := ivNoise(fs.Arg(0))
	if err != nil {
		return err
	}
	return writeJSON(*out, map[string]any{
		"iv_from_tick_rounded_marks_vs_deribit_iv": noise,
		"journal": map[string]any{"frames": frames, "mark_messages": marks, "mark_rows": rows, "index_ticks": idx,
			"options_in_summary": len(summary.Summary), "duration_s": float64(lastTS-firstTS) / 1e3,
			"rows_per_mark_message": float64(rows) / math.Max(1, float64(marks)), "mark_message_interval_ms": mkdist(gaps)},
		"parity_forward_vs_deribit":      map[string]any{"abs_diff_bps": mkdist(fdiff), "expiries": fwd},
		"implied_vol_vs_deribit_mark_iv": ivs,
	})
}

// ivNoise streams the journal through an engine (for parity forwards) and, for every WebSocket
// mark row of an option with T >= 1 day, inverts mark x forward and compares with the row's own
// Deribit IV, bucketed by the mark's size in 0.0001-BTC ticks.
func ivNoise(path string) (map[string]any, error) {
	jr, err := deribit.Open(path)
	if err != nil {
		return nil, err
	}
	defer jr.Close()
	e := engine.New(engine.Config{Pricer: pricing.Go{}})
	p := pricing.Default()
	buckets := []struct {
		name   string
		lo, hi float64
	}{{"1-9 ticks", 1, 10}, {"10-99 ticks", 10, 100}, {"100-999 ticks", 100, 1000}, {">=1000 ticks", 1000, math.Inf(1)}}
	diffs := make([][]float64, len(buckets))
	var in []pricing.IVIn
	var ref, ticks []float64
	for {
		l, err := jr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		ev, err := deribit.Decode(l)
		if err != nil {
			return nil, err
		}
		e.Apply(&ev)
		if ev.Kind != deribit.KindMarks {
			continue
		}
		in, ref, ticks = in[:0], ref[:0], ticks[:0]
		for _, m := range ev.Marks {
			f, t, k, call, ok := e.Quote(m.Name)
			if !ok || t < 1.0/365 || !(m.Mark > 0) || !(m.IV > 0) {
				continue
			}
			if (call && k < f) || (!call && k >= f) {
				continue // OTM only
			}
			in = append(in, pricing.IVIn{Call: call, Price: m.Mark * f, F: f, K: k, T: t})
			ref = append(ref, m.IV)
			ticks = append(ticks, math.Round(m.Mark/1e-4))
		}
		vol, ok := make([]float64, len(in)), make([]bool, len(in))
		p.IV(in, vol, ok)
		for i := range in {
			if !ok[i] {
				continue
			}
			for b, bk := range buckets {
				if ticks[i] >= bk.lo && ticks[i] < bk.hi {
					diffs[b] = append(diffs[b], 100*math.Abs(vol[i]-ref[i]))
				}
			}
		}
	}
	out := map[string]any{"pricer": p.Name(), "note": "OTM options, T >= 1 day; forward = parity forward; abs diff in vol points"}
	for b, bk := range buckets {
		out[bk.name] = mkdist(diffs[b])
	}
	return out, nil
}
