// Normal distribution functions with attention to the tails.
#pragma once

#include <cmath>
#include <limits>

namespace volsurf {

inline constexpr double kSqrt2 = 1.41421356237309504880;
inline constexpr double kSqrt2Pi = 2.50662827463100050242;
inline constexpr double kInvSqrt2Pi = 0.39894228040143267794;
inline constexpr double kSqrtPi = 1.77245385090551602730;

inline double npdf(double x) { return kInvSqrt2Pi * std::exp(-0.5 * x * x); }

// Phi(x) via erfc, accurate in the lower tail (relative accuracy for x << 0).
inline double ncdf(double x) { return 0.5 * std::erfc(-x / kSqrt2); }

// Scaled complementary error function erfcx(z) = exp(z^2) erfc(z), for z >= 0 without overflow.
// Direct evaluation up to z = 6; Lentz continued fraction beyond (erfc underflows there).
inline double erfcx(double z) {
  if (z < 6.0) return std::exp(z * z) * std::erfc(z);
  // erfc(z) = exp(-z^2)/sqrt(pi) * 1/(z + 1/2/(z + 1/(z + 3/2/(z + 2/(z + ...)))))
  double f = z, c = z, d = 0.0;
  const double tiny = 1e-300;
  for (int n = 1; n < 200; ++n) {
    const double a = 0.5 * n;
    d = z + a * d;
    if (std::fabs(d) < tiny) d = tiny;
    c = z + a / c;
    if (std::fabs(c) < tiny) c = tiny;
    d = 1.0 / d;
    const double delta = c * d;
    f *= delta;
    if (std::fabs(delta - 1.0) < 1e-16) break;
  }
  return 1.0 / (kSqrtPi * f);
}

// Inverse normal CDF: Acklam's rational approximation refined by one Halley step (|err| ~ 1e-15).
inline double inv_ncdf(double p) {
  if (p <= 0.0) return -std::numeric_limits<double>::infinity();
  if (p >= 1.0) return std::numeric_limits<double>::infinity();
  static constexpr double a[] = {-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02,
                                 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00};
  static constexpr double b[] = {-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02,
                                 6.680131188771972e+01, -1.328068155288572e+01};
  static constexpr double c[] = {-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00,
                                 -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00};
  static constexpr double d[] = {7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00,
                                 3.754408661907416e+00};
  double x;
  if (p < 0.02425) {
    const double q = std::sqrt(-2 * std::log(p));
    x = (((((c[0] * q + c[1]) * q + c[2]) * q + c[3]) * q + c[4]) * q + c[5]) / ((((d[0] * q + d[1]) * q + d[2]) * q + d[3]) * q + 1);
  } else if (p > 1 - 0.02425) {
    const double q = std::sqrt(-2 * std::log1p(-p));
    x = -(((((c[0] * q + c[1]) * q + c[2]) * q + c[3]) * q + c[4]) * q + c[5]) / ((((d[0] * q + d[1]) * q + d[2]) * q + d[3]) * q + 1);
  } else {
    const double q = p - 0.5, r = q * q;
    x = (((((a[0] * r + a[1]) * r + a[2]) * r + a[3]) * r + a[4]) * r + a[5]) * q /
        (((((b[0] * r + b[1]) * r + b[2]) * r + b[3]) * r + b[4]) * r + 1);
  }
  const double e = ncdf(x) - p;
  const double u = e * kSqrt2Pi * std::exp(0.5 * x * x);
  return x - u / (1 + 0.5 * x * u);
}

}  // namespace volsurf
