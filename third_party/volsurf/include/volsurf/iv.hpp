// Implied volatility in Jäckel's normalised coordinates ("Let's Be Rational", 2015, §2-3).
//
// Normalisation: x = ln(F/K), s = vol * sqrt(T), beta = price / (df * sqrt(F K)). Every option is
// reduced to an out-of-the-money normalised call with x <= 0 (put-call parity removes the
// intrinsic value; b(x,s,put) = b(-x,s,call) handles puts), where
//
//   b(x, s) = Phi(x/s + s/2) e^{x/2} - Phi(x/s - s/2) e^{-x/2},
//   b'(s)   = e^{-(h^2+t^2)/2} / sqrt(2 pi),    h = x/s, t = s/2,
//   b''(s)  = b'(s) (x^2/s^3 - s/4).
//
// Below the inflection point s_c = sqrt(2|x|) both normal CDF arguments are negative and b is
// computed from scaled complementary error functions, e^{-(h^2+t^2)/2} [erfcx(a) - erfcx(b)] / 2,
// which neither underflows nor loses the leading digits to a catastrophic subtraction until s is
// tiny. There the solver iterates on ln b (Jäckel's lower-region transformation), above it on b,
// with third-order Householder steps inside a maintained bracket and bisection as a fallback.
//
// This is not the full LBR algorithm (no rational initial guesses, so ~7 iterations on average
// instead of 2). Accuracy is tested against the problem's conditioning: the vol error stays below
// the error implied by a few ulps of price noise divided by vega (tests/test_bs_iv.cpp).
#pragma once

#include <cmath>
#include <limits>

#include "volsurf/bs.hpp"
#include "volsurf/normal.hpp"

namespace volsurf {

struct IVResult {
  double vol;
  int iterations;
  bool ok;
};

// Solve b(x, s) = beta for s, with x <= 0 and 0 < beta < e^{x/2}.
inline IVResult solve_normalised(double beta, double x) {
  const double bmax = std::exp(0.5 * x);
  if (!(beta > 0) || !(beta < bmax)) return {std::numeric_limits<double>::quiet_NaN(), 0, false};
  const double sc = std::sqrt(2.0 * std::fabs(x));
  const double bc = sc > 0 ? norm_call_otm(x, sc) : 0.0;
  const bool lower = beta < bc;
  // Upper regime: price close to its bound, iterate on ln(bound - b). Middle: iterate on b.
  const bool upper = !lower && beta > 0.5 * bmax;
  // Bracket: [lo, hi] with b(lo) < beta < b(hi).
  double lo = lower ? 0.0 : sc, hi = lower ? sc : std::fmax(sc, 1.0);
  if (!lower)
    while (norm_call_otm(x, hi) < beta) {
      lo = hi;
      hi *= 2;
      if (hi > 1e3) break;
    }
  double s = lower ? sc * 0.5 : (beta > 0.5 * bmax ? hi : 0.5 * (lo + hi));
  if (sc == 0) s = std::fmax(kSqrt2Pi * beta, 1e-8);  // ATM: b ~ s / sqrt(2 pi)
  const double lb = std::log(beta);
  const double lc = std::log(bmax - beta);  // upper region target for ln(complement)
  for (int it = 1; it <= 100; ++it) {
    const double v = norm_vega(x, s);
    const double h2 = x * x / (s * s * s) - 0.25 * s;  // b''/b'
    double step;
    if (lower) {
      // f = ln b - ln beta;  f' = v/b;  f'' = (v h2 b - v^2)/b^2.
      const double b = norm_call_otm(x, s);
      if (b < beta) lo = s;
      else hi = s;
      if (!(b > 0)) {
        step = 0.5 * (hi - s);
      } else {
        const double f = std::log(b) - lb, f1 = v / b, f2 = (v * h2 * b - v * v) / (b * b);
        const double n = -f / f1;
        step = n / (1.0 + 0.5 * n * f2 / f1);  // Halley
      }
    } else if (!upper) {
      const double b = norm_call_otm(x, s);
      if (b < beta) lo = s;
      else hi = s;
      const double n = -(b - beta) / v;
      step = n / (1.0 + 0.5 * n * h2);  // Halley on b with b''/b' = h2
    } else {
      // g = ln C - ln C_beta with C = bmax - b computed directly (no cancellation);
      // g' = -v/C;  g'' = (-v h2 C - v^2)/C^2.
      const double c = norm_call_complement(x, s);
      if (c > bmax - beta) lo = s;
      else hi = s;
      if (!(c > 0)) {
        step = 0.5 * (lo - s);
      } else {
        const double g = std::log(c) - lc, g1 = -v / c, g2 = (-v * h2 * c - v * v) / (c * c);
        const double n = -g / g1;
        step = n / (1.0 + 0.5 * n * g2 / g1);
      }
    }
    double sn = s + step;
    if (!(sn > lo && sn < hi) || !std::isfinite(sn)) sn = 0.5 * (lo + hi);  // safeguard
    if (std::fabs(sn - s) <= 1e-15 * s || hi - lo <= 2e-16 * hi) return {sn, it, true};
    s = sn;
  }
  return {s, 100, true};
}

// Implied Black-76 volatility from an option price. Returns NaN outside no-arbitrage bounds.
inline IVResult implied_vol_black(Opt o, double price, double F, double K, double T, double df = 1.0) {
  const double nan = std::numeric_limits<double>::quiet_NaN();
  if (!(T > 0) || !(F > 0) || !(K > 0) || !(df > 0)) return {nan, 0, false};
  double x = std::log(F / K);
  double beta = price / (df * std::sqrt(F * K));
  double th = static_cast<double>(o);
  // Remove intrinsic (normalised) if in the money: OTM = price - intrinsic, switch type.
  // Normalised intrinsic e^{x/2} - e^{-x/2} = 2 sinh(x/2): one rounding instead of three.
  const double intrinsic = std::fmax(th * 2.0 * std::sinh(0.5 * x), 0.0);
  if (th * x > 0) {
    beta -= intrinsic;
    th = -th;
  }
  // Time value at or (within rounding) below zero: vol 0 if within 4 ulps of the price, else
  // the price violates the intrinsic bound.
  const double eps = 4 * std::numeric_limits<double>::epsilon() * std::fmax(price / (df * std::sqrt(F * K)), 1e-300);
  if (beta <= eps) return {beta >= -eps ? 0.0 : nan, 0, beta >= -eps};
  // Map an OTM put (x >= 0) to an OTM call with -x.
  if (th < 0) x = -x;
  IVResult r = solve_normalised(beta, x);
  r.vol /= std::sqrt(T);
  return r;
}

inline IVResult implied_vol_bsm(Opt o, double price, double S, double K, double T, double r, double q) {
  return implied_vol_black(o, price, S * std::exp((r - q) * T), K, T, std::exp(-r * T));
}

}  // namespace volsurf
