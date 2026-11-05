// Command bellhopd is the Bellhop daemon: it owns all state and child
// processes and serves the local API used by the CLI and GUI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nietthijmen/bellhop/internal/daemon"
	"github.com/nietthijmen/bellhop/internal/dns"
	"github.com/nietthijmen/bellhop/pkg/version"
)

func main() {
	if os.Getenv(dns.EnvActive) == "1" {
		os.Exit(runDNS(os.Stderr))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// runDNS is the supervised resolver process. A clean shutdown exits 0 so
// the supervisor does not treat a stop as a crash.
func runDNS(stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := dns.Run(ctx, os.Getenv(dns.EnvAddr), os.Getenv(dns.EnvTLD))
	if err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "%s: dns: %v\n", version.ID+"d", err)
		return 1
	}
	return 0
}

func run(args []string, stdout, stderr io.Writer) int {
	name := version.ID + "d"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	foreground := fs.Bool("foreground", true, "also log human-readable output to stderr (the JSON log file is always written)")
	showVersion := fs.Bool("version", false, "print version information and exit")
	logLevel := fs.String("log-level", "info", "minimum log level: debug, info, warn or error")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags]\n\nRuns the %s daemon until interrupted.\n\nFlags:\n", name, version.Name)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "%s: unexpected argument %q; run %s -help for usage\n", name, fs.Arg(0), name)
		return 2
	}
	if *showVersion {
		v := version.Get()
		_, _ = fmt.Fprintf(stdout, "%s %s (commit %s, built %s, %s, %s/%s)\n", name, v.Version, v.Commit, v.Date, v.Go, v.OS, v.Arch)
		return 0
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: invalid -log-level %q: use debug, info, warn or error\n", name, *logLevel)
		return 2
	}

	// os.Interrupt is Ctrl+C everywhere; SIGTERM comes from launchd/systemd
	// and, on Windows, from console close/logoff/shutdown events.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second signal terminates immediately
	}()

	err := daemon.Run(ctx, daemon.Options{
		Foreground:       *foreground,
		Stderr:           stderr,
		LogLevel:         level,
		SetDefaultLogger: true,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}
