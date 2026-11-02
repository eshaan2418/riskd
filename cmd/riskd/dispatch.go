package main

import "fmt"

func dispatch(cmd string, args []string) error {
	switch cmd {
	case "run":
		return cmdRun(args)
	case "replay":
		return cmdReplay(args)
	case "report":
		return cmdReport(args)
	case "bench":
		return cmdBench(args)
	}
	return fmt.Errorf("unknown command %q", cmd)
}
