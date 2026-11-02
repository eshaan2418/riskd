// Package instr parses Deribit option instrument names and computes year fractions.
package instr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Instrument is a listed option, e.g. BTC-29JAN27-102000-C.
type Instrument struct {
	Name     string
	Currency string
	Expiry   time.Time // 08:00 UTC on the expiry date
	ExpKey   string    // e.g. 29JAN27
	Strike   float64
	Call     bool
}

// Parse decodes CUR-DMMMYY-STRIKE-{C,P}. Strikes may use 'd' as a decimal point (Deribit uses
// e.g. 1d5 for 1.5 on some underlyings).
func Parse(name string) (Instrument, error) {
	p := strings.Split(name, "-")
	if len(p) != 4 {
		return Instrument{}, fmt.Errorf("instr: %q: want 4 fields", name)
	}
	d, err := time.Parse("2Jan06", p[1])
	if err != nil {
		return Instrument{}, fmt.Errorf("instr: %q: expiry: %w", name, err)
	}
	k, err := strconv.ParseFloat(strings.ReplaceAll(p[2], "d", "."), 64)
	if err != nil || !(k > 0) {
		return Instrument{}, fmt.Errorf("instr: %q: bad strike", name)
	}
	var call bool
	switch p[3] {
	case "C":
		call = true
	case "P":
	default:
		return Instrument{}, fmt.Errorf("instr: %q: type must be C or P", name)
	}
	exp := time.Date(d.Year(), d.Month(), d.Day(), 8, 0, 0, 0, time.UTC)
	return Instrument{Name: name, Currency: p[0], Expiry: exp, ExpKey: p[1], Strike: k, Call: call}, nil
}

// MsPerYear is ACT/365 in milliseconds, the convention Deribit uses for option time to expiry.
const MsPerYear = 365 * 24 * 3600 * 1000.0

// YearFrac returns the time from tsMs (Unix milliseconds) to expiry in years (may be <= 0).
func (i Instrument) YearFrac(tsMs int64) float64 {
	return float64(i.Expiry.UnixMilli()-tsMs) / MsPerYear
}
