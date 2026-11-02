# Market structure notes: real-time options risk on Deribit

## What the feed gives you
- **Marks, not trades.** Deribit's `mark_price` is the exchange's fair value for each option. It
  is used for margin and liquidation, and it is computed from Deribit's own vol surface. Fitting
  risk to marks instead of to sparse trades or wide quotes gives a smooth, complete surface. The
  catch is that you inherit the exchange's model.
- **Changed rows only.** Each `markprice.options` message carries only the options whose mark
  changed, 39 rows per message on average, about one message per second, out of ~900 listed options in the 45-minute recorded session. An engine that revalues only
  what changed does far less work than one that reprices everything on a timer.
- **No forward in the stream.** The mark channel has no underlying price, and the spot index is
  not the forward: BTC futures trade at a basis that varies by expiry. riskd implies each
  expiry's forward from the marks themselves by put-call parity. See below for how close that
  gets to Deribit's own number.

## Inverse options
BTC options are quoted, margined and settled in BTC. Consequences:
1. **USD value.** Value = mark × F. Under Black-76 the BTC mark is V/F. This is the convention
   that makes riskd's implied vols match Deribit's.
2. **Parity.** C − P = F − K becomes c − p = 1 − K/F in BTC, so F = K / (1 − (c − p)).
3. **Premium-adjusted delta.** An account that holds the premium in BTC is also exposed to BTC
   through the premium itself. A BTC-denominated trader's delta is the Black delta minus V/F
  . riskd reports the USD (Black) delta and hedges on it, which is the right
   choice for a USD-based book. A BTC-based book would hedge the premium-adjusted delta.

## Tick rounding and the PnL explain
Marks are rounded to 0.0001 BTC, about $8 at BTC ≈ 78k.
- **IV noise.** For an option worth a few ticks, a one-tick move in the mark is a large relative
  price change, and so a large implied-vol change: vega is small and Δσ ≈ tick·F / vega.
  Inverting the tick-rounded mark gives an IV that jitters around Deribit's own (unrounded) IV.
  See `research/results/report.json` for the measured noise by mark size.
- **Why the explain notices.** A risk system attributes each revaluation's PnL as
  ν·dσ + ½·volga·dσ² + …
  - Pure noise in σ has mean-zero dσ, but dσ² is always positive. The volga term therefore
    accumulates a systematic bias, and the vega term picks up large offsetting swings.
  - The "unexplained" bucket is the third-order remainder of those noisy jumps.
  - This is not real PnL. It is an artefact of inverting rounded prices.
- **The fix.** Take the vol from a source that is not rounded, here Deribit's `iv` field, and
  value at it. The replay comparison in the README quantifies the difference on the same
  session.

  Desks do the equivalent: they mark at a fitted surface (SVI, for example) rather than inverting
  each rounded quote. The surface is smooth, so noise averages out across strikes.

## Forwards from parity
- **Error per strike.** Rounding each of c and p by up to half a tick moves the implied F by up to
  about F²/K × 1e-4 per strike, roughly $8, or 1 bp.
- **Median.** The median over 5 near-the-money strikes cuts this further. Against Deribit's own
  `underlying_price` the parity forward agrees to about 0.1 bp (see the report).
- **Basis.** That is far below the basis between expiries, which is tens to hundreds of bps.
  Using the index as every expiry's forward would be a much larger error.

## What a real options-desk risk system adds
- **Portfolio margin.** Deribit PM is a scenario-based margin: price and vol shocks per
  underlying, with extended-loss scenarios. riskd's scenario grid has the same shape but not
  Deribit's parameters.
- **Vega bucketing by tenor and moneyness**, with term-structure-weighted vega, because short
  expiries move more per vol point.
- **Smile dynamics in scenarios.** Sticky strike, used here, versus sticky moneyness or delta:
  for a large spot move the choice changes the scenario PnL of a skewed book materially.
- **Real fills.** Real fills from an OMS (or drop copy) instead of the simulated blotter, with
  fees, plus a reconciliation of the engine's positions against the exchange's.

## Data provenance and conduct
- **Public data only.** Only public market-data channels are used (no API key, no account), and
  the recorded journals are raw public frames.
- **Simulated book.** The demo book is synthetic. Its "fills" are simulated at marks plus an
  assumed spread and are never sent anywhere.
- **Not investment advice.** Educational/research project. Not investment advice. No live
  trading.
