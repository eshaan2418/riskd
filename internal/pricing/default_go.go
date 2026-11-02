//go:build !volsurf

package pricing

// Default is the pricer the engine uses unless told otherwise.
func Default() Pricer { return Go{} }

// Available lists the compiled-in pricers.
func Available() []Pricer { return []Pricer{Go{}} }
