# riskd design

## Shape
```
Deribit public WS ──► reader goroutine ──► chan Event (1024) ──► engine goroutine ──► atomic.Pointer[Snapshot]
  (markprice.options,     decode JSON,                            Apply: marks/index,        │
   price_index)           journal frame                           forwards, IV, Greeks,      ├─► HTTP JSON  (/api/*)
REST book summary ──────► (first event)                           explain, strategy,         ├─► SSE stream (/api/stream)
                                                                  scenarios, limits          └─► /metrics
journal.jsonl.gz ──────► replay (same Apply path, exchange timestamps)
```

**One writer.** The engine goroutine owns all mutable state, so there are no locks on the hot
path.

**Readers.** Readers load the latest immutable `Snapshot` through an `atomic.Pointer`. A slow
HTTP client can never stall the engine: SSE subscribers have a one-slot "newest wins" mailbox.

**Event time.** The engine runs on exchange timestamps. Time to expiry, theta and the strategy's
trade schedule all come from event time, never from the wall clock. A replay is therefore not an
approximation of the live run: it is the same computation.

## Inputs
| channel | content | rate (recorded session) |
|---|---|---|
| REST `get_book_summary_by_currency` | every option's mark, mark IV and Deribit's `underlying_price` | once, at start |
| `markprice.options.btc_usd` | changed option marks: `mark_price` (BTC), `iv` | ~1 message/s, 39 rows on average |
| `deribit_price_index.btc_usd` | spot index | ~1/s |

These are public market-data channels with no authentication. Nothing in this repository can
place an order.

## Valuation
- **Price.** Deribit options are inverse: premium is in BTC. With Black-76 USD price V, the BTC
  mark is V/F, so the engine values every option at `V = mark × F`.
- **Forward per expiry.** Implied from the marks by inverse put-call parity,
  F = K / (1 − (c − p)), taking the median over the 5 strikes nearest the money. Marks are
  rounded to 0.0001 BTC; the median suppresses the ~$8 rounding noise per strike. Deribit's own
  `underlying_price` from the REST summary is kept only as a validation reference.
- **Implied vol.** Two sources, selectable with `-vol`:
  - `mark` (default): invert `mark × F` with the pricer. The engine's vol is then consistent with
    the price it reports.
  - `deribit`: use Deribit's `iv` field, which is not tick-rounded. See MICROSTRUCTURE for why
    this matters for PnL explain.

  If inversion fails (a tick-rounded deep-ITM mark at or below intrinsic), the engine falls back
  to Deribit's IV and counts it.
- **Greeks.** Black-76 Δ, Γ, ν, Θ, vanna and volga per unit, with respect to the expiry's
  forward. Book delta and gamma are mapped to the index assuming proportional forwards
  (dF/dI = F/I): delta is reported in BTC, and gamma as BTC of delta per 1% index move.

## Pricers
`pricing.Pricer` is a batch interface (`Greeks([]Opt, []Greeks)`, `IV([]IVIn, []float64, []bool)`).
- **`go`.** Pure Go: textbook d1/d2 Greeks, and Newton IV from the Manaster-Koehler start with a
  bisection safeguard.
- **`volsurf`.** Built with `-tags volsurf`: the [volsurf](https://github.com/eshaan2418/volsurf)
  C++ library through cgo. It provides the normalised Black price, accurate deep out of the money,
  and the three-regime Halley IV.
  - The slice headers are passed straight to C, with no copying or marshalling. A layout test
    pins the struct layouts the shim relies on.
  - One cgo call per batch amortises the ~tens-of-ns cgo transition.
  - The headers are vendored in `third_party/volsurf` (MIT).

The two pricers are differentially tested against each other. The pure-Go build has no C
toolchain dependency (`CGO_ENABLED=0` works).

## Incremental state
- **Dirty sets.** A marks event touches some instruments. Their expiries are marked dirty, and
  each dirty expiry recomputes its parity forward. If the forward moved, every position in that
  expiry is dirty, because its F changed. Otherwise only positions whose own mark changed are
  dirty.
- **One batch.** All dirty positions are revalued in one pricer batch: IV batch, then Greeks
  batch.
- **Aggregates.** Each per-expiry aggregate (qty-weighted value and Greeks) is updated by
  subtracting the old contribution and adding the new one. A test checks the incremental
  aggregates against a from-scratch sum after hundreds of random events and trades.
- **Book totals.** These are a sum over expiries (tens), computed per snapshot.

## PnL and explain
- **Accounting.** Positions use average-cost accounting. Realised plus unrealised PnL equals
  `q·V − Σ fills`.
- **Explain at each revaluation.** For each position, using the previous Greeks:

      ΔV ≈ Δ·dF + ½Γ·dF² + ν·dσ + Θ·dt + vanna·dF·dσ + ½volga·dσ²,   unexplained = ΔV_actual − Σ terms

- **Fills.** A fill adds `qty × (model value − fill price)` to *new-trade PnL*, the edge or cost of
  trading away from the mark.
- **Identity.** Total PnL equals the sum of all explain components plus new-trade PnL. This is
  tested through opens, flips and hedges.
- **Spot hedge.** It contributes `q·dI` to delta PnL, exactly.

## Scenarios
- **Grid.** Full revaluation on a grid of forward shocks (−20% … +20%) × vol shocks (−10 … +10
  vol points), sticky strike. It runs every second of event time.
- **Parallelism.** Positions are split into fixed 256-position chunks. Workers take chunks, and
  partial grids are summed in chunk order. The result is bit-identical for any worker count
  (tested), which keeps the state hash meaningful.

## Limits
Delta (BTC), gamma per 1%, vega per expiry bucket and worst scenario loss. A breach raises an
alert. It clears only when utilisation falls below 90% (hysteresis), so a value hovering at the
limit does not flap. Alerts carry event timestamps.

## The demo book
There is no real portfolio here, so `DemoBook` simulates one:
- It opens N option positions (near-the-money, T ≥ 1 day, mark ≥ 0.001 BTC; random size of
  0.1–5 BTC, long or short) at mark ± an assumed half-spread of max(1% of mark, 1 tick).
- It trades a random option every 20 s of event time.
- It delta-hedges in spot at index ± 1 bp when |delta| > 5 BTC.

It is seeded and driven by event time, so it is part of the deterministic computation. It sends
no orders anywhere.

## Determinism and the state hash
`StateHash` is FNV-1a over the exact float bits of every position (qty, cost, realised, F, T,
IV, price, Δ, ν), the hedge, the explain and the latest scenario grid.
- **Live vs replay.** A live run journals every frame it processes. Replaying the journal with
  the same flags produces the same hash. CI replays the committed sample with different
  scenario worker counts and compares hashes.

## Not here
Exchange-accurate portfolio margin (Deribit's SPAN-like PM), inverse-premium delta adjustment in
the hedge (reported conceptually in MICROSTRUCTURE), smile dynamics in scenarios (sticky
moneyness / SABR backbone), persistence of positions across restarts, authentication on the
HTTP API.
