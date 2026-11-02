package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/eshaan2418/riskd/internal/deribit"
	"github.com/eshaan2418/riskd/internal/engine"
	"github.com/eshaan2418/riskd/internal/server"
)

type result struct {
	Mode         string              `json:"mode"`
	Pricer       string              `json:"pricer"`
	Source       string              `json:"source,omitempty"`
	Events       uint64              `json:"events"`
	MarkRows     uint64              `json:"mark_rows"`
	Seconds      float64             `json:"wall_seconds"`
	EventsPerSec float64             `json:"events_per_sec"`
	RowsPerSec   float64             `json:"mark_rows_per_sec"`
	Proc         *engine.LatencyView `json:"processing_latency"`
	Wire         *engine.LatencyView `json:"receive_to_publish_latency,omitempty"`
	Hash         string              `json:"state_hash"`
	Final        *engine.Snapshot    `json:"final"`
}

func finish(mode, src string, r *engine.Runner, start time.Time) result {
	s := r.Snapshot()
	sec := time.Since(start).Seconds()
	p, w := r.Latencies()
	fin := *s
	fin.Positions = nil
	return result{Mode: mode, Pricer: s.Pricer, Source: src, Events: s.Stats.Events, MarkRows: s.Stats.MarkRows, Seconds: sec,
		EventsPerSec: float64(s.Stats.Events) / sec, RowsPerSec: float64(s.Stats.MarkRows) / sec, Proc: p, Wire: w, Hash: s.Hash, Final: &fin}
}

func writeJSON(path string, v any) error {
	var w io.Writer = os.Stdout
	if path != "" && path != "-" {
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// replayInto feeds a journal into the runner. speed 0 = as fast as possible; 1 = the recorded
// pace (by receive timestamps); k = k times faster.
func replayInto(ctx context.Context, path string, r *engine.Runner, speed float64) error {
	jr, err := deribit.Open(path)
	if err != nil {
		return err
	}
	defer jr.Close()
	var first int64
	start := time.Now()
	for ctx.Err() == nil {
		l, err := jr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		ev, err := deribit.Decode(l)
		if err != nil {
			return err
		}
		if speed > 0 {
			if first == 0 {
				first = l.RecvNs
			}
			due := start.Add(time.Duration(float64(l.RecvNs-first) / speed))
			if d := time.Until(due); d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					return nil
				}
			}
		}
		ev.RecvNs = 0 // processing latency only; receive times are from another run
		r.Step(&ev)
	}
	return nil
}

func cmdReplay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	out := fs.String("out", "-", "result JSON path")
	prof := fs.String("cpuprofile", "", "write a CPU profile")
	ef := addEngineFlags(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: riskd replay [flags] JOURNAL")
	}
	if *prof != "" {
		f, err := os.Create(*prof)
		if err != nil {
			return err
		}
		_ = pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	cfg, err := ef.config()
	if err != nil {
		return err
	}
	r := engine.NewRunner(engine.New(cfg), false)
	start := time.Now()
	if err := replayInto(context.Background(), fs.Arg(0), r, 0); err != nil {
		return err
	}
	return writeJSON(*out, finish("replay", fs.Arg(0), r, start))
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "HTTP listen address")
	replay := fs.String("replay", "", "replay this journal instead of connecting live")
	speed := fs.Float64("speed", 1, "replay speed (0: as fast as possible)")
	record := fs.String("record", "", "live: also record the session to this journal")
	dur := fs.Duration("dur", 0, "stop after this long (0: until interrupted)")
	out := fs.String("out", "", "write the final result JSON here")
	cur := fs.String("currency", "BTC", "BTC or ETH")
	ef := addEngineFlags(fs)
	_ = fs.Parse(args)
	cfg, err := ef.config()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *dur > 0 {
		var c2 context.CancelFunc
		ctx, c2 = context.WithTimeout(ctx, *dur)
		defer c2()
	}
	r := engine.NewRunner(engine.New(cfg), *replay == "")
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: server.Handler(r), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "riskd: serving http://%s/api/snapshot (pricer %s)\n", ln.Addr(), cfg.Pricer.Name())
	start := time.Now()
	mode := "live"
	if *replay != "" {
		mode = "replay-paced"
		err = replayInto(ctx, *replay, r, *speed)
	} else {
		err = runLive(ctx, *cur, *record, r)
	}
	if err != nil {
		return err
	}
	res := finish(mode, *replay, r, start)
	fmt.Fprintf(os.Stderr, "riskd: %d events, state hash %s\n", res.Events, res.Hash)
	if *out != "" {
		return writeJSON(*out, res)
	}
	return nil
}

// runLive streams Deribit into the engine. Frames are journaled (if recording) and decoded on
// the reader goroutine, then handed to the engine goroutine in order through a buffered channel.
func runLive(ctx context.Context, cur, record string, r *engine.Runner) error {
	var jw *deribit.Writer
	if record != "" {
		var err error
		if jw, err = deribit.Create(record); err != nil {
			return err
		}
		defer jw.Close()
	}
	events := make(chan deribit.Event, 1024)
	done := make(chan struct{})
	go func() { r.Run(context.Background(), events); close(done) }()
	summary, err := deribit.FetchSummary(ctx, cur)
	if err != nil {
		close(events)
		<-done
		return err
	}
	now := time.Now().UnixNano()
	if jw != nil {
		_ = jw.Write("rest", now, summary)
	}
	ev, err := deribit.Decode(deribit.Line{RecvNs: now, Kind: "rest", Msg: summary})
	if err != nil {
		close(events)
		<-done
		return err
	}
	events <- ev
	err = deribit.Stream(ctx, cur, func(recv int64, msg []byte) error {
		if jw != nil {
			if err := jw.Write("ws", recv, msg); err != nil {
				return err
			}
		}
		ev, err := deribit.DecodeWS(recv, msg)
		if err != nil {
			return err
		}
		if ev.Kind != deribit.KindNone {
			events <- ev
		}
		return nil
	})
	close(events)
	<-done
	return err
}
