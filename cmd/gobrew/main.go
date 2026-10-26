package main

import (
	"context"
	"os"
	"os/signal"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal requests a safe cancellation; a second uses the default
	// process behavior if shutdown itself is stuck.
	go func() {
		<-ctx.Done()
		stop()
	}()
	return cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, cli.BuildInfo{Version: version, Commit: commit, Date: date})
}
