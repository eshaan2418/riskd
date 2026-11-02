# riskd: deep-dive interview questions

### 1. Why a single-writer engine instead of locks?
- **One goroutine owns the state.** Every update (a mark, a fill, a forward move) touches
  several pieces of shared state: the position, the per-expiry aggregate, the explain, the
  limits. With locks you need a consistent ordering and still pay for contention on every
  read.
- **Readers are free.** Readers get an immutable snapshot through an atomic pointer load. A
  reader cannot see a half-applied event and cannot slow the writer.
- **Determinism falls out.** One writer, events in journal order, so replay equals live.

The cost is that the writer's throughput bounds the system. The benchmarks show it has
headroom of several orders of magnitude over the feed rate.

### 2. How do you prove replay equals live?
- **Same inputs.** The live run journals every frame before decoding, and the engine runs only
  on exchange timestamps and journal order.
- **Seeded strategy.** The simulated strategy is seeded and scheduled in event time.
- **Exact hash.** `StateHash` hashes the exact float bits of every position, the hedge, the
  explain and the scenario grid. The live run's final hash equals the replay's.
- **Workers don't matter.** The scenario grid sums fixed-size chunks in chunk order, so the
  hash does not depend on the worker count either (tested, and checked in CI on the committed
  sample).

### 3. Why imply the forward from parity instead of using the index?
BTC futures trade at a basis that differs by expiry, tens to hundreds of bps over months.
Pricing every expiry off the index would mis-price every option and mis-state delta. Parity on
the marks gives each expiry's forward from data already on the wire: F = K / (1 − (c − p)) for
inverse options. The median over 5 near-the-money strikes agrees with Deribit's own
`underlying_price` to about 0.1 bp on the recorded session.

### 4. Why does the explain go wrong when you invert tick-rounded marks?
An option marked at 3 ticks has huge relative rounding error, so its inverted IV jumps by vol
points when the mark moves one tick. Explain attributes ν·dσ + ½·volga·dσ². With noisy σ,
dσ² > 0 always, so volga PnL accumulates a spurious bias, and the third-order remainder shows up
as unexplained. Using Deribit's unrounded IV (`-vol deribit`) removes it. The README compares
the two on the same recorded session. On a desk, the same fix is "mark to a fitted surface, not
to individual quotes".

### 5. Why use a batch pricer interface?
- **cgo.** Each Go→C transition costs tens of nanoseconds, comparable to one Greeks evaluation.
  One call per batch amortises it (the benchmark shows the per-option cost by batch size).
- **Pure Go too.** Batching keeps the inner loop tight and allocation-free.
- **One shape for every caller.** The scenario grid and full revaluation are naturally batches.

### 6. How does the incremental update stay correct?
- **Diff the aggregates.** Each per-expiry aggregate is `Σ qty × greek`. When a position is
  revalued, subtract its old contribution and add the new one; a fill adds `dq × greek`.
- **Accumulated float error.** This is bounded and small (tested to 1e-8 relative against a
  from-scratch sum after hundreds of events).
- **Desk practice.** A production system would also periodically recompute from scratch and
  alert on drift.
- **Forward moves.** The trap is that a forward change dirties every position in that expiry,
  not only those whose mark changed. riskd handles this with expiry-level dirty flags.

### 7. Why average-cost accounting, and what is "new-trade PnL"?
- **Average cost** is common on desks. It makes realised PnL path-independent of lot matching.
  FIFO would give the same total PnL with a different realised/unrealised split.
- **New-trade PnL** = qty × (mark value − fill price). It is the edge (or cost) of trading away
  from the mark. Without it the explain would not add up: a fill changes total PnL immediately
  by exactly that amount.

### 8. What is the PnL identity you test?
Cash-based PnL is q·V − Σ(dq·p). Between fills it changes by q·dV, which is the explain
(Taylor terms plus unexplained). At a fill it changes by dq·(V − p), which is new-trade PnL. So
realised + unrealised = Σ explain + Σ new-trade, exactly. The test checks it to 1e-6 relative
through opens, increases, reductions, flips and spot hedges.

### 9. Why should unexplained PnL be third order, and how do you test it?
With Greeks through second order (including the cross term vanna), the Taylor remainder is
O(dF³, dσ³, dF²dσ, …, dt²). The test runs the same synthetic market with 60-second and 15-second
steps. Under diffusive scaling, dF ~ √dt, so the remainder per step should fall by about
4^1.5 = 8 or more. It falls by 25×. If a Greek were wrong, the remainder would be first or
second order and the ratio would collapse to about 2–4.

### 10. Sticky strike versus sticky moneyness in the scenario grid?
- **Sticky strike** (used here): each option keeps its own implied vol when spot moves.
- **Sticky moneyness:** vol is a function of K/F, so after a spot move each strike picks up the
  vol of its new moneyness.
- **Why it matters.** For a skewed smile (puts richer), a spot drop under sticky moneyness
  lowers the vol of a given strike, and a big shock gives a very different PnL. Desks usually
  report both, or use a model-implied smile dynamic such as SABR's backbone.

### 11. What does the delta in BTC mean for inverse options?
riskd's delta is ∂V_USD/∂Index summed over the book, the BTC amount whose USD value moves like
the book. A USD-based account hedges this. An account holding BTC collateral also has the
premium's own BTC exposure, so its hedge would use the premium-adjusted delta (Black delta
minus V/F per option).

### 12. Why are forward-based Greeks mapped to the index with F/I?
Each option's Black delta is with respect to its expiry's forward. To add deltas across
expiries you need a common underlying. Assuming forwards move proportionally with the index
(constant basis in percentage terms), dF/dI = F/I, so delta_I = delta_F·F/I and
gamma_I = gamma_F·(F/I)². Basis risk, forwards moving differently from the index, is a separate
risk that this mapping ignores.

### 13. How does the SSE stream avoid back-pressure on the engine?
Each subscriber has a channel of capacity one. Publishing is non-blocking: if the slot is full,
drop the stale snapshot and put the newest. A slow client sees fewer, but always current,
snapshots. The engine never waits on a socket.

### 14. Why is a limit breach not cleared at 100%?
A value oscillating around the limit would raise and clear an alert on every tick, which is
alert fatigue. The breach clears only below 90% utilisation (hysteresis), and re-breaches only
above 100%. This is tested with an explicit path.

### 15. Why two pricers?
- **Portability.** The pure-Go one keeps the service buildable without a C toolchain
  (`CGO_ENABLED=0`).
- **Robustness.** volsurf's IV solver uses transformed objectives in the wings and never fails
  where a price carries information.
- **The test makes the difference concrete.** On a 32-standard-deviation out-of-the-money option,
  the textbook Newton crawls and gives up at 100 iterations, whereas volsurf converges to the
  exact vol.
- **Agreement.** On the real Deribit chain both agree to well under 1e-3 vol points, and they
  are differentially tested on 50,000 random options.

### 16. What happens at expiry?
The demo freezes positions at their last valuation once T ≤ 0; there was no expiry inside the
recorded window. A production system would settle against Deribit's delivery price, a
30-minute TWAP of the index before 08:00 UTC, and roll the position into cash.

### 17. Where does the time go per event?
- **The scenario grid dominates.** It is 6,750 full Black-76 revaluations for a 150-position
  book, about 290 µs on one core. With the grid disabled the engine's p50 is 37 µs.
- **After chunking fix.** With 32-position chunks over 4 workers, the p50 is 156 µs on the
  45-minute replay.
- **Outside the engine.** gzip inflate and JSON decoding of the mark frames run on the reader
  goroutine in live mode.

### 18. Why is the state hash only reproducible on the same architecture?
- **FMA fusion.** Go's spec allows the compiler to fuse x*y + z into a fused multiply-add, which
  it does on arm64 but not on baseline amd64. Clang contracts FMAs in the volsurf C++ the same
  way.
- **Different rounding.** A fused result is rounded once instead of twice, so the last bit can
  differ, and the hash compares exact bits.
- **Consequence.** Live == replay holds on any one machine (CI checks replay against replay on
  each runner), but the committed live hash is only expected to match on darwin/arm64, where the
  session was recorded.
- **Cross-platform alternative.** Forbid contraction (explicit `float64()` conversions in Go,
  `-ffp-contract=off` in C++), or hash values rounded to a tolerance.

### 19. How would you scale to many underlyings and books?
Partition by underlying, one engine per currency, since the forwards and Greeks are independent.
A cross-underlying aggregate (USD totals, correlated scenarios) then becomes a second-level
consumer of the per-underlying snapshots. Within one underlying the scenario grid is the only
heavy computation, and it is already parallel and deterministic.

### 20. What would you monitor in production?
- Feed staleness: time since the last mark per expiry.
- Forward-vs-reference deviation.
- IV fallback rate.
- Unexplained PnL as a fraction of gross explained. A jump means a bad mark or a Greek bug.
- Event processing latency p99.
- Limit utilisation.
- Snapshot hash continuity across restarts, via journal replay.

All except staleness are exported on `/metrics`.

### 21. What is not modelled, and why does it matter?
- Deribit's portfolio-margin parameters.
- Fees.
- Fills against the real book: the spread is assumed.
- Smile dynamics in the scenarios.
- Basis risk between index and forwards.
- The inverse-premium delta in the hedge.

Each one changes real PnL or risk. They are the next items on a desk-grade roadmap, but none of
them changes the engine's architecture.
