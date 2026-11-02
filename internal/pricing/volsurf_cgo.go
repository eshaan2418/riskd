//go:build volsurf

package pricing

/*
#cgo CXXFLAGS: -std=c++20 -O3 -I${SRCDIR}/../../third_party/volsurf/include
#include <stddef.h>
#include <stdint.h>
typedef struct { uint8_t call; double F, K, T, vol; } vs_opt;
typedef struct { double price, delta, gamma, vega, theta, vanna, volga; } vs_greeks;
typedef struct { uint8_t call; double price, F, K, T; } vs_ivin;
void vs_greeks_batch(const vs_opt* in, vs_greeks* out, size_t n);
void vs_iv_batch(const vs_ivin* in, double* vol, uint8_t* ok, size_t n);
*/
import "C"

import "unsafe"

// Volsurf calls the vendored volsurf C++ library: normalised Black-76 Greeks and the
// three-regime Halley implied-vol solver.
type Volsurf struct{}

func (Volsurf) Name() string { return "volsurf" }

func (Volsurf) Greeks(in []Opt, out []Greeks) {
	if len(in) == 0 {
		return
	}
	C.vs_greeks_batch((*C.vs_opt)(unsafe.Pointer(&in[0])), (*C.vs_greeks)(unsafe.Pointer(&out[0])), C.size_t(len(in)))
}

func (Volsurf) IV(in []IVIn, vol []float64, ok []bool) {
	if len(in) == 0 {
		return
	}
	C.vs_iv_batch((*C.vs_ivin)(unsafe.Pointer(&in[0])), (*C.double)(unsafe.Pointer(&vol[0])),
		(*C.uint8_t)(unsafe.Pointer(&ok[0])), C.size_t(len(in)))
}

// Default is the pricer the engine uses unless told otherwise.
func Default() Pricer { return Volsurf{} }

// Available lists the compiled-in pricers.
func Available() []Pricer { return []Pricer{Go{}, Volsurf{}} }
