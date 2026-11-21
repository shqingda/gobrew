package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/shqingda/gobrew/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	// The first signal requests a safe cancellation and says so at once, since
	// the current step may take a while to finish. A second one uses the
	// default process behavior if shutdown itself is stuck.
	go func() {
		<-signals
		signal.Reset(os.Interrupt, syscall.SIGTERM)
		if !jsonOutput(os.Args[1:]) {
			fmt.Fprintln(os.Stderr, "\nStopping after the current step; press Ctrl-C again to quit at once.")
		}
		cancel()
	}()
	return cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, cli.BuildInfo{Version: version, Commit: commit, Date: date})
}

// jsonOutput keeps stderr empty for --json, as the CLI does.
func jsonOutput(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--json" || strings.HasPrefix(arg, "--json=") {
			return true
		}
	}
	return false
}
