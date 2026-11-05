// Command bellhop is the command line client for the bellhopd daemon.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/nietthijmen/bellhop/internal/cli"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/platform"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, os.Args[1:], cli.Options{
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		DefaultSocket: ipc.DefaultAddress(platform.MustPaths()),
	})
	stop()
	os.Exit(code)
}
