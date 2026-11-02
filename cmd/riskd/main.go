// Command riskd is a real-time options risk and PnL service over public Deribit market data.
package main

import (
	"fmt"
	"os"
)

const usage = `usage: riskd <command> [flags]
  record   record public Deribit option marks + index to a journal
  run      run the risk service (live, or --replay JOURNAL) with the HTTP API
  replay   replay a journal as fast as possible and print the final state hash
  report   accuracy report: parity forwards and implied vols vs Deribit
  bench    engine, pricer and scenario benchmarks`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "record":
		err = cmdRecord(os.Args[2:])
	default:
		err = dispatch(os.Args[1], os.Args[2:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "riskd:", err)
		os.Exit(1)
	}
}
