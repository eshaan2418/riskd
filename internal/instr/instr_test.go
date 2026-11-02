package instr

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	i, err := Parse("BTC-3NOV26-78000-P")
	if err != nil {
		t.Fatal(err)
	}
	if i.Strike != 78000 || i.Call || i.ExpKey != "3NOV26" || !i.Expiry.Equal(time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", i)
	}
	j, err := Parse("BTC-29JAN27-102000-C")
	if err != nil || !j.Call || j.Expiry.Year() != 2027 {
		t.Fatalf("%+v %v", j, err)
	}
	if x, err := Parse("XRP-27NOV26-1d5-C"); err != nil || x.Strike != 1.5 {
		t.Fatalf("%+v %v", x, err)
	}
	for _, bad := range []string{"BTC-PERPETUAL", "BTC-32NOV26-1-C", "BTC-3NOV26-0-C", "BTC-3NOV26-1-X", "BTC-3NOV26-abc-C"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
	// One day before expiry = 1/365 years.
	if yf := i.YearFrac(i.Expiry.Add(-24 * time.Hour).UnixMilli()); yf != 1.0/365 {
		t.Fatal(yf)
	}
}
