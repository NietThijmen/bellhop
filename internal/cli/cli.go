// Package cli implements the user-facing command line client. Every command
// is a thin call to the daemon API over internal/ipc; nothing here talks to
// services or the store directly.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// Exit codes. They are part of the CLI contract: scripts rely on them.
const (
	ExitOK          = 0 // success
	ExitUser        = 1 // bad arguments, or the daemon rejected the request
	ExitUnreachable = 2 // the daemon could not be reached
	ExitJobFailed   = 3 // a long-running job failed or was cancelled
)

// ExitError carries an explicit exit code and an optional hint.
type ExitError struct {
	Code int
	Err  error
	Hint string
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// usageError marks bad command-line usage (exit 1, with a --help hint).
type usageError struct {
	cmd string
	err error
}

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// Options configures Execute.
type Options struct {
	Stdout io.Writer // defaults to os.Stdout
	Stderr io.Writer // defaults to os.Stderr
	// DefaultSocket is the default for --socket. When empty, the address is
	// resolved from platform.Paths() on first use.
	DefaultSocket string
}

// app holds the global flags shared by every command.
type app struct {
	json   bool
	socket string
	now    func() time.Time
}

// client returns an IPC client for the configured address.
func (a *app) client() (*ipc.Client, error) {
	addr := a.socket
	if addr == "" {
		d, err := platform.Paths()
		if err != nil {
			return nil, fmt.Errorf("cannot work out the daemon address: %w (pass --socket explicitly)", err)
		}
		addr = ipc.DefaultAddress(d)
	}
	return ipc.NewClient(addr), nil
}

// Help groups. PHP commands stay unregistered until the PHP HTTP API exists;
// the group is attached so those commands can join it later.
const (
	groupSites = "sites"
	groupPHP   = "php"
)

// NewRootCommand builds the command tree. The returned app-level state is
// private; use Execute to run it with exit-code mapping.
func NewRootCommand(opts Options) *cobra.Command {
	root, _ := newRoot(opts)
	return root
}

func newRoot(opts Options) (*cobra.Command, *app) {
	a := &app{now: time.Now}
	root := &cobra.Command{
		Use:   version.ID,
		Short: version.Name + " manages local PHP sites and their services",
		Long: version.Name + " manages local PHP sites, PHP versions, databases and mail.\n" +
			"Every command talks to the " + version.ID + "d daemon, which must be running.",
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			HiddenDefaultCmd: true,
		},
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "print machine-readable JSON instead of text")
	root.PersistentFlags().StringVar(&a.socket, "socket", opts.DefaultSocket, "daemon IPC address (Unix socket path or Windows named pipe)")
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &usageError{cmd: c.CommandPath(), err: err}
	})

	root.AddGroup(
		&cobra.Group{ID: groupSites, Title: "Site Commands:"},
		&cobra.Group{ID: groupPHP, Title: "PHP Commands:"},
	)
	root.AddCommand(
		a.statusCmd(),
		a.serviceActionCmd("start", "Started"),
		a.serviceActionCmd("stop", "Stopped"),
		a.serviceActionCmd("restart", "Restarted"),
		a.logsCmd(),
		a.versionCmd(),
		a.parkCmd(),
		a.forgetCmd(),
		a.pathsCmd(),
		a.linkCmd(),
		a.unlinkCmd(),
		a.linksCmd(),
		a.sitesCmd(),
		a.openCmd(),
		a.secureCmd(true),
		a.secureCmd(false),
	)
	return root, a
}

// Execute runs the CLI with args (without the program name) and returns the
// process exit code. Errors are printed to stderr, or as a JSON error object
// on stdout when --json is set.
func Execute(ctx context.Context, args []string, opts Options) int {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	root, a := newRoot(opts)
	root.SetArgs(args)
	root.SetOut(opts.Stdout)
	root.SetErr(opts.Stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	r := describe(err)
	if a.json {
		_ = writeJSON(opts.Stdout, api.ErrorResponse{Error: api.Error{Code: r.code, Message: r.msg, Hint: r.hint}}, true)
	} else {
		_, _ = fmt.Fprintf(opts.Stderr, "%s: %s\n", version.ID, r.msg)
		if r.hint != "" {
			_, _ = fmt.Fprintln(opts.Stderr, r.hint)
		}
	}
	return r.exit
}

// ExitCode maps an error returned by a command to the process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	return describe(err).exit
}

type rendered struct {
	exit int
	code string // api error code (or "unreachable"/"job_failed"/"usage")
	msg  string
	hint string
}

const (
	codeUnreachable = "unreachable"
	codeJobFailed   = "job_failed"
	codeUsage       = "usage"
	codeInterrupted = "interrupted"
)

// unreachableHint tells the user how to start the daemon.
var unreachableHint = fmt.Sprintf("Start it: run '%sd' in a terminal, or '%s setup' to install the background service.",
	version.ID, version.ID)

func describe(err error) rendered {
	var ee *ExitError
	if errors.As(err, &ee) {
		r := describe(ee.Err)
		r.exit = ee.Code
		if ee.Code == ExitJobFailed {
			r.code = codeJobFailed
		}
		if ee.Hint != "" {
			r.hint = ee.Hint
		}
		return r
	}
	if errors.Is(err, ipc.ErrUnreachable) {
		return rendered{exit: ExitUnreachable, code: codeUnreachable,
			msg: err.Error(), hint: unreachableHint}
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return rendered{exit: ExitUser, code: ae.Code, msg: ae.Message, hint: ae.Hint}
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return rendered{exit: ExitUser, code: codeUsage, msg: ue.Error(),
			hint: fmt.Sprintf("Run '%s --help' for usage.", ue.cmd)}
	}
	if errors.Is(err, context.Canceled) {
		return rendered{exit: ExitUser, code: codeInterrupted, msg: "interrupted"}
	}
	return rendered{exit: ExitUser, code: api.CodeInternal, msg: err.Error()}
}

// maxArgs is cobra.MaximumNArgs with a usage error.
func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return &usageError{cmd: cmd.CommandPath(),
				err: fmt.Errorf("%s accepts at most %d argument(s), got %d", cmd.CommandPath(), n, len(args))}
		}
		return nil
	}
}

// noArgs is cobra.NoArgs with a usage error.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return &usageError{cmd: cmd.CommandPath(),
			err: fmt.Errorf("%s takes no arguments, got %q", cmd.CommandPath(), args[0])}
	}
	return nil
}

// writeJSON encodes v to w as one JSON value, indented when indent is set.
func writeJSON(w io.Writer, v any, indent bool) error {
	enc := json.NewEncoder(w)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write JSON output: %w", err)
	}
	return nil
}
