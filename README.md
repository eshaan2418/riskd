# riskd

**Real-time options risk and PnL service in Go over live public Deribit BTC option data.** It
computes parity-implied forwards, implied vols and Greeks (via the
[volsurf](https://github.com/eshaan2418/volsurf) C++ library through cgo, or pure Go), an
incremental PnL explain, a full-revaluation scenario grid and limits with hysteresis. It serves
results over HTTP, SSE and Prometheus metrics, and every session can be replayed bit for bit.

[![CI](https://github.com/eshaan2418/riskd/actions/workflows/ci.yml/badge.svg)](https://github.com/eshaan2418/riskd/actions/workflows/ci.yml)

> Educational/research project. Not investment advice. No live trading.
> Public market-data channels only (no API key, no account). The option book is simulated; no
> order is ever sent anywhere.

## Headline results

Apple M5, Go 1.27.3. Every number comes from a file in [`research/results`](research/results) or
[`bench/results`](bench/results).

| what | result |
|---|---|
| **Live == replay** on a real 10-minute Deribit session (1,128 events, 27,820 mark rows, 179 simulated fills) | final state hash `442054851b34cdc8` from both the live run and the replay of its journal; reproduced by a test on the committed journal |
| Parity-implied forwards vs Deribit's own `underlying_price` (12 expiries) | median **0.13 bp**, max 0.15 bp |
| Implied vols vs Deribit `mark_iv` (421 OTM options, T ≥ 1 day) | median **0.003** vol pts, max 0.021 (both pricers) |
| Spurious PnL from inverting tick-rounded marks, 45-minute session | vega +7,230 USD and volga −8,202 USD (offsetting noise), unexplained −81 USD. With Deribit's unrounded IV: vega −477, volga −0.63, **unexplained −0.04 USD** |
| Event processing latency, live (10 min) | p50 **173 µs**, p99 549 µs; receive-to-snapshot p50 213 µs, p99 660 µs |
| Engine headroom | 45-minute session replays in 1.0 s, more than 2,000× the feed rate |
| Full book revaluation (synthetic, 29,304 positions) | 17.4 ms with volsurf (594 ns/position), 29.0 ms pure Go |
| Scenario grid, 45 cells × 29,304 positions (1.3M revaluations) | 10.1 ms on 18 goroutines, 70 ms on 1 |
| Greeks per option, batch 4,096 | Go 30.3 ns, volsurf 48.3 ns (cgo; also computes the robust normalised price) |
| Implied vol per solve | volsurf **355 ns**, Go 555 ns |

## What it does

```
Deribit public WS (markprice.options.btc_usd, deribit_price_index.btc_usd) + REST book summary
   │  reader goroutine: journal frame → decode
   ▼
single-writer engine (exchange time):
   parity forward per expiry ─► IV from mark×F (volsurf / Go) ─► Black-76 Greeks
   ─► incremental per-expiry aggregates ─► PnL explain (Δ Γ ν Θ vanna volga + unexplained + new-trade)
   ─► demo book (simulated fills, delta hedge) ─► scenario grid (±20% × ±10 vol) ─► limits/alerts
   │  atomic.Pointer[Snapshot]
   ▼
GET /api/snapshot  /api/positions  /api/scenarios  /api/limits   SSE /api/stream   /metrics
```

- **Forwards from the wire.** The mark channel has no forward, and the index is not the forward,
  because of basis. riskd implies each expiry's forward from inverse put-call parity on the
  marks: F = K / (1 − (c − p)), median over the 5 nearest strikes.
- **Incremental and exact.**
  - Only changed marks, plus every position of an expiry whose forward moved, are revalued, in
    one pricer batch.
  - Aggregates are updated by difference and checked against a from-scratch sum.
  - Total PnL equals the sum of the explain components to 1e-6 (tested through opens, flips and
    hedges).
- **Explain you can trust.** Unexplained PnL is the third-order remainder: shrinking the step 4×
  shrinks it 25× (test). On real data the explain revealed a microstructure effect (below).
- **Deterministic.**
  - Exchange time everywhere, a seeded strategy scheduled in event time, and a scenario grid
    summed in fixed chunks.
  - The state hash covers the exact float bits of every position, the explain and the grid.
  - A fake-exchange test drives the real live path (REST plus WebSocket with heartbeats) and
    replays its journal to the same hash.
- **Two pricers, one interface.**
  - **volsurf through cgo** (`-tags volsurf`, the default in the Makefile): zero-copy slices, one
    cgo call per batch.
  - **Pure Go** (`CGO_ENABLED=0` builds).
  - The two are differentially tested on 50,000 options. On a 32-σ out-of-the-money option the Go
    Newton solver gives up, while volsurf recovers the vol exactly.

## The tick-rounding finding

Deribit marks are rounded to 0.0001 BTC (~$8). Inverting `mark × F` gives an IV whose noise
depends on how many ticks the option is worth. The table covers OTM options with T ≥ 1 day, from
every mark row in the 45-minute session ([`report.json`](research/results/report.json)):

| mark size | rows | median abs(IV − Deribit IV) | p99 |
|---|---|---|---|
| 1–9 ticks | 293 | 0.61 vol pts | 4.13 |
| 10–99 ticks | 3,420 | 0.15 | 0.46 |
| 100–999 ticks | 11,679 | 0.023 | 0.22 |
| ≥ 1,000 ticks | 1,772 | 0.013 | 0.037 |

The PnL explain attributes ν·dσ + ½·volga·dσ². With noisy σ, dσ² is always positive, so volga
accumulates a bias and vega swings to offset it. Same journal, same book:

| 45-min session | delta | vega | volga | unexplained |
|---|---|---|---|---|
| `-vol mark` (invert rounded mark) | −975 | +7,230 | −8,202 | −81.11 |
| `-vol deribit` (Deribit's IV) | −987 | −477 | −0.63 | −0.04 |

A desk sees the same thing when it marks to individual quotes instead of a fitted surface.
See [MICROSTRUCTURE](docs/MICROSTRUCTURE.md).

## Demo (real output)

The server replays the committed live journal at 10× speed. Excerpt from
[`demo_output.txt`](research/results/demo_output.txt):

```
$ curl -s 127.0.0.1:18090/api/snapshot | jq '{index, totals, pnl, ...}'
  "index": 78197.51,
  "totals": { "value_usd": -130750.02, "delta_btc": 1.7725, "gamma_1pct_btc": 1.0813,
              "vega_usd_per_volpt": -1040.31, "theta_usd_per_day": -1987.88, ... },
  "pnl": { "total_usd": -34333.98, "realised_usd": -32.00, "unrealised_usd": -34301.98,
           "explain": { "Delta": -21.51, "Gamma": 0.84, "Vega": 237.67, "Theta": -3.24,
                        "Vanna": 0.64, "Volga": -109.38, "Unexplained": -3.92, "NewTrade": -34435.08 } },
  "limits": [ {"name": "delta_btc", "utilisation": 0.443, "breached": false}, ... ],
  "worst": -11026.42,
  "latency": { "count": 292, "p50_us": 181.5, "p99_us": 653.8 }

$ curl -s 127.0.0.1:18090/metrics | grep -E '^riskd_(delta|vega|pnl)'
riskd_delta_btc 1.7493962786505972
riskd_vega_usd -1055.1236004357604
riskd_pnl_usd -34460.30913753032
riskd_pnl_unexplained_usd -4.02814145014068
```

The large negative new-trade PnL is the simulated cost of opening 150 positions across an
assumed half-spread of max(1% of mark, 1 tick). It is an assumption of the demo book, not a
property of the engine.

## Run it

```bash
make build          # go build -tags volsurf (cgo; needs a C++20 compiler). TAGS= for pure Go
make test           # all tests, both pricers
make race           # race detector
make demo           # serve the API over the committed journal: http://127.0.0.1:8080/api/snapshot
make data           # record a public Deribit session (DUR=45m)
make report replay  # accuracy report + explain comparison on the recorded session
make bench          # pricer + engine benchmarks -> bench/results/bench.json
make fuzz lint
bin/riskd run -dur 10m -record s.jsonl.gz -out live.json   # live: serve, record, report final hash
bin/riskd replay s.jsonl.gz                                # same hash
```

## Layout
```
cmd/riskd            record | run | replay | report | bench
internal/deribit     public WS/REST client, gzip JSONL journal, frame decoder (fuzzed)
internal/instr       instrument names, ACT/365 year fractions
internal/forward     inverse put-call-parity forwards
internal/pricing     Pricer interface; pure Go; volsurf cgo shim (third_party/volsurf, MIT)
internal/engine      engine, explain, scenarios, limits, demo book, snapshot/hash, runner
internal/server      HTTP JSON, SSE, Prometheus text metrics
internal/sim         synthetic Deribit-shaped markets for tests and stress benchmarks
```

## Documentation
- [docs/DESIGN.md](docs/DESIGN.md): architecture, valuation, incremental state, determinism
- [docs/MICROSTRUCTURE.md](docs/MICROSTRUCTURE.md): inverse options, parity forwards, tick
  rounding, what a desk adds
- [docs/DEEP_DIVE.md](docs/DEEP_DIVE.md): 21 interview-style questions and answers
- [docs/PERF_LOG.md](docs/PERF_LOG.md): latency breakdown, the scenario-chunking fix, and the bugs
  found

## Where it fits
riskd is the risk layer of a small research trading stack. volsurf is its pricing core. The
upstream projects are [itchbook](https://github.com/eshaan2418/itchbook),
[marketmux](https://github.com/eshaan2418/marketmux),
[matchbox](https://github.com/eshaan2418/matchbox),
[raftex](https://github.com/eshaan2418/raftex),
[queuesim](https://github.com/eshaan2418/queuesim) and
[tickstore](https://github.com/eshaan2418/tickstore).

## Data
- The public Deribit market-data endpoints are used with no authentication.
- The committed journal (`testdata/`, 0.3 MB) holds raw public frames from a 10-minute session on
  2026-11-02.
- The 45-minute session journal is not committed. Its SHA-256 is in
  `research/results/journal_sha256.txt`, and `make data` records a new one.

## License
MIT. `third_party/volsurf` is MIT (same author).
