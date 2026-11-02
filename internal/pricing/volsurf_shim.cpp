//go:build volsurf

// C ABI over the header-only volsurf library for cgo. Struct layouts mirror pricing.Opt,
// pricing.Greeks and pricing.IVIn (Go bool is one byte; doubles are 8-byte aligned).
#include <cstddef>
#include <cstdint>

#include "volsurf/bs.hpp"
#include "volsurf/iv.hpp"

extern "C" {
struct vs_opt { std::uint8_t call; double F, K, T, vol; };
struct vs_greeks { double price, delta, gamma, vega, theta, vanna, volga; };
struct vs_ivin { std::uint8_t call; double price, F, K, T; };

void vs_greeks_batch(const vs_opt* in, vs_greeks* out, std::size_t n) {
  for (std::size_t i = 0; i < n; ++i) {
    const vs_opt& o = in[i];
    const auto t = o.call ? volsurf::Opt::Call : volsurf::Opt::Put;
    if (!(o.T > 0) || !(o.vol > 0)) {
      const double in_c = o.F > o.K ? o.F - o.K : 0.0, in_p = o.K > o.F ? o.K - o.F : 0.0;
      const double d = o.F > o.K ? 1.0 : 0.0;
      out[i] = o.call ? vs_greeks{in_c, d, 0, 0, 0, 0, 0} : vs_greeks{in_p, d - 1, 0, 0, 0, 0, 0};
      continue;
    }
    const volsurf::Greeks g = volsurf::bsm_greeks(t, o.F, o.K, o.T, 0.0, 0.0, o.vol);
    // Price from the robust normalised path (accurate deep OTM); Greeks from the closed forms.
    out[i] = {volsurf::black76(t, o.F, o.K, o.T, o.vol), g.delta, g.gamma, g.vega, g.theta, g.vanna, g.volga};
  }
}

void vs_iv_batch(const vs_ivin* in, double* vol, std::uint8_t* ok, std::size_t n) {
  for (std::size_t i = 0; i < n; ++i) {
    const vs_ivin& q = in[i];
    const auto r = volsurf::implied_vol_black(q.call ? volsurf::Opt::Call : volsurf::Opt::Put, q.price, q.F, q.K, q.T);
    vol[i] = r.vol;
    ok[i] = r.ok && r.vol > 0;
  }
}
}
