package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eshaan2418/riskd/internal/deribit"
)

func cmdRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	out := fs.String("out", "deribit.jsonl.gz", "journal path")
	dur := fs.Duration("dur", 10*time.Minute, "recording length")
	cur := fs.String("currency", "BTC", "BTC or ETH")
	_ = fs.Parse(args)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, cancel2 := context.WithTimeout(ctx, *dur)
	defer cancel2()
	w, err := deribit.Create(*out)
	if err != nil {
		return err
	}
	summary, err := deribit.FetchSummary(ctx, *cur)
	if err != nil {
		return err
	}
	if err := w.Write("rest", time.Now().UnixNano(), summary); err != nil {
		return err
	}
	n := 0
	start := time.Now()
	err = deribit.Stream(ctx, *cur, func(recv int64, msg []byte) error {
		n++
		if n%300 == 0 {
			fmt.Fprintf(os.Stderr, "record: %d frames, %s\n", n, time.Since(start).Round(time.Second))
		}
		return w.Write("ws", recv, msg)
	})
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	fmt.Fprintf(os.Stderr, "record: %d frames in %s -> %s\n", n, time.Since(start).Round(time.Second), *out)
	return err
}
