package pricing

import (
	"testing"
	"unsafe"
)

// The cgo shim reinterprets Go slices as C arrays; pin the layouts it relies on.
func TestLayout(t *testing.T) {
	if unsafe.Sizeof(Opt{}) != 40 || unsafe.Offsetof(Opt{}.F) != 8 || unsafe.Offsetof(Opt{}.Vol) != 32 {
		t.Fatal("Opt layout")
	}
	if unsafe.Sizeof(Greeks{}) != 56 || unsafe.Sizeof(IVIn{}) != 40 || unsafe.Offsetof(IVIn{}.Price) != 8 {
		t.Fatal("Greeks/IVIn layout")
	}
	if unsafe.Sizeof(true) != 1 {
		t.Fatal("bool size")
	}
}
