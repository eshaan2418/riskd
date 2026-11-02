# riskd performance and correctness log

Machine: Apple M5 (18 CPUs), macOS, Go 1.27.3, volsurf via cgo with Apple clang -O3.

Committed results:
- `research/results/`: report, replays, live runs.
- `bench/results/bench.json`: synthetic books.

The machine is shared with an unrelated CPU-heavy workload, so the load average is recorded next
to each benchmark where it matters.

## Feed versus engine budget
- **Feed rate** in the 45-minute session (`report.json`): 2,568 mark messages (39 rows each on
  average, one every ~1 s) and 2,367 index ticks. That is about 2 events per second.
- **Engine headroom.** Replaying the same session takes 1.1 s of wall time, so the engine runs
  more than 2,000× faster than the feed. The latency that matters is per event, not throughput.

## Per-event latency: where it went

| change | p50 | p99 | note |
|---|---|---|---|
| initial (scenario grid every 1 s of event time, 4 workers, 256-position chunks) | 331 µs | 658 µs | 45-min replay |
| same, scenario grid disabled (`-scenario-every 1000h`) | 37 µs | 173 µs | the grid is ~90% of the cost |
| chunk size 256 → 32 (1 worker) | 336 µs | 654 µs | unchanged, as expected |
| chunk size 32, 4 workers | **159 µs** | **437 µs** | a 150-position book now spans 5 chunks |
| chunk size 32, 9 workers | 155 µs | 478 µs | no gain past 5 chunks |

**The bug.** Chunking is fixed-size so that the scenario sums, and the state hash, do not depend
on the worker count. With 256-position chunks, a 150-position book was a single chunk, so the
"4 workers" setting had nothing to parallelise. Smaller chunks fixed it, and the hash is
identical for 1, 4 and 9 workers (`9b89482ff384a820` on the 45-min session in each case).

**The cost.** The grid itself is 150 positions × 45 cells = 6,750 Black-76 valuations, about
290 µs on one core, or ~43 ns per valuation.

**Where the rest goes.** In the CPU profile of the 45-min replay, everything outside the engine
is gzip inflate (13%) and JSON decoding. Those costs sit on the reader goroutine in live mode.

## Pricers (`bench/results/bench.json`)
See the README table. The design points:
- **volsurf Greeks cost more than Go's.** The shim computes volsurf's robust normalised price in
  addition to the closed-form Greeks: two evaluations against Go's one. That is the price of
  accurate deep-OTM values. Batching amortises the cgo transition; a batch of 1 shows the
  overhead.
- **volsurf IV is faster** than the Go Newton solver. It converges in fewer iterations because
  of the Halley steps and regime transforms, and it never fails on prices that carry
  information. The Go solver gives up on a 32-standard-deviation option (pricing test).

## Correctness journey
| issue | symptom | fix |
|---|---|---|
| Journal writer omitted each line's closing brace | journal round-trip test failed (unit test, before any real data was used); a live recording in progress had to be restarted | write `}\n`; the round-trip test now checks every error |
| Go IV returned `ok=true` after 100 non-converged iterations | deep-wing test: vol 1.377 instead of 0.6 | report non-convergence as failure |
| SSE handler sent headers only with the first event | client blocked in `Do()` until data arrived; stream test timed out | flush headers immediately |
| Vol from tick-rounded marks | 45-min session: vega PnL +7,230 and volga −8,202 USD cancelling, unexplained −81 USD | `-vol deribit`: vega −477, volga −0.63, unexplained −0.04 USD |
| Scenario chunking | see above | 32-position chunks |
| Finite-difference test tolerances | flaky failures on deep-ITM prices | tolerances include rounding noise, about ε·P/h² for second differences |
