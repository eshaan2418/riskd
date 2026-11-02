// Black-Scholes-Merton (spot, continuous rates/dividend) and Black-76 (forward) prices and Greeks.
#pragma once

#include <cmath>

#include "volsurf/normal.hpp"

namespace volsurf {

enum class Opt : int { Call = 1, Put = -1 };

struct Greeks {
  double price, delta, gamma, vega, theta, rho, vanna, volga;
};

// Normalised OTM call price, x <= 0.
inline double norm_call_otm(double x, double s) {
  if (s <= 0) return 0.0;
  const double h = x / s, t = 0.5 * s;
  if (h + t < 0) {
    const double a = -(h + t) / kSqrt2, c = -(h - t) / kSqrt2;  // 0 < a < c
    return 0.5 * std::exp(-0.5 * (h * h + t * t)) * (erfcx(a) - erfcx(c));
  }
  // Above the inflection point |h| < t. Writing b = 2 sinh(x/2) Phi(h+t) + e^{-x/2} [Phi(h+t) -
  // Phi(h-t)] with the bracket as an erf difference avoids subtracting two numbers near 0.5 (which
  // costs ~ulp(0.5)/b relative accuracy at small s, e.g. 1e-13 for short-dated ATM options).
  const double dphi = 0.5 * (std::erf((h + t) / kSqrt2) - std::erf((h - t) / kSqrt2));
  return 2.0 * std::sinh(0.5 * x) * ncdf(h + t) + std::exp(-0.5 * x) * dphi;
}

// Complement e^{x/2} - b(x, s) = e^{x/2} Phi(-h-t) + e^{-x/2} Phi(h-t), computed without
// cancellation (used in the upper region where b approaches its bound).
inline double norm_call_complement(double x, double s) {
  const double h = x / s, t = 0.5 * s;
  return std::exp(0.5 * x) * ncdf(-h - t) + std::exp(-0.5 * x) * ncdf(h - t);
}

inline double norm_vega(double x, double s) {
  const double h = x / s, t = 0.5 * s;
  return kInvSqrt2Pi * std::exp(-0.5 * (h * h + t * t));
}

// Black-76 on a forward F with discount factor df. Computed as df * sqrt(FK) * [normalised OTM
// price] + intrinsic, which stays accurate to full relative precision deep out of the money where
// the textbook F N(d1) - K N(d2) loses most of its digits to cancellation.
inline double black76(Opt o, double F, double K, double T, double vol, double df = 1.0) {
  const double th = static_cast<double>(o);
  if (T <= 0 || vol <= 0) return df * std::fmax(th * (F - K), 0.0);
  const double s = vol * std::sqrt(T);
  const double x = std::log(F / K);
  // OTM normalised value: call with x <= 0, or put with x >= 0 (= call at -x); ITM adds intrinsic.
  const double otm = norm_call_otm(-std::fabs(x), s);
  const bool itm = th * x > 0;
  const double intrinsic = itm ? th * (F - K) : 0.0;
  return df * (std::sqrt(F * K) * otm + intrinsic);
}

// Black-Scholes-Merton: spot S, rate r, dividend/borrow yield q.
inline double bsm(Opt o, double S, double K, double T, double r, double q, double vol) {
  return black76(o, S * std::exp((r - q) * T), K, T, vol, std::exp(-r * T));
}

// All first- and second-order Greeks analytically (per unit vol for vega/vanna/volga, per year
// for theta, per unit rate for rho).
inline Greeks bsm_greeks(Opt o, double S, double K, double T, double r, double q, double vol) {
  const double th = static_cast<double>(o);
  const double sqT = std::sqrt(T), s = vol * sqT;
  const double dq = std::exp(-q * T), dr = std::exp(-r * T);
  const double d1 = (std::log(S / K) + (r - q + 0.5 * vol * vol) * T) / s, d2 = d1 - s;
  const double Nd1 = ncdf(th * d1), Nd2 = ncdf(th * d2), pd1 = npdf(d1);
  Greeks g{};
  g.price = th * (S * dq * Nd1 - K * dr * Nd2);
  g.delta = th * dq * Nd1;
  g.gamma = dq * pd1 / (S * s);
  g.vega = S * dq * pd1 * sqT;
  g.theta = -S * dq * pd1 * vol / (2 * sqT) - th * r * K * dr * Nd2 + th * q * S * dq * Nd1;
  g.rho = th * K * T * dr * Nd2;
  g.vanna = -dq * pd1 * d2 / vol;
  g.volga = g.vega * d1 * d2 / vol;
  return g;
}

}  // namespace volsurf
