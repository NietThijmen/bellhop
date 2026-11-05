package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// maxFollowReconnects bounds how often logs -f resubscribes after the daemon
// ends the stream without delivering anything.
const maxFollowReconnects = 3

// allLogs is the --json output of `logs` without a service.
type allLogs struct {
	Services []api.LogsResponse `json:"services"`
}

func (a *app) logsCmd() *cobra.Command {
	var (
		lines  int
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "logs [service]",
		Short: "Show service logs, for one service or all of them",
		Long: "Show the most recent log lines of a service, or of every service when none is given.\n" +
			"With -f, keep printing new lines until interrupted (Ctrl-C).\n" +
			"With --json and -f, every line is printed as one JSON object per line.",
		Example: fmt.Sprintf("  %[1]s logs mariadb -n 50\n  %[1]s logs proxy -f", version.ID),
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if lines < 0 {
				return &usageError{cmd: cmd.CommandPath(), err: fmt.Errorf("-n must be 0 or more, got %d", lines)}
			}
			service := ""
			if len(args) == 1 {
				service = args[0]
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			return a.runLogs(cmd, c, service, lines, follow)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "number of recent lines to show (0 = daemon default)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines until interrupted")
	return cmd
}

// svcLine is a log line tagged with its service.
type svcLine struct {
	service string
	line    api.LogLine
}

type lineKey struct {
	service string
	nanos   int64
	stream  string
	text    string
}

func keyOf(service string, l api.LogLine) lineKey {
	return lineKey{service: service, nanos: l.Time.UnixNano(), stream: l.Stream, text: l.Line}
}

func (a *app) runLogs(cmd *cobra.Command, c *ipc.Client, service string, n int, follow bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	// Subscribe before reading the tail so no line falls between the two;
	// duplicates are filtered below.
	var (
		evs  <-chan api.Event
		errc <-chan error
	)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if follow {
		evs, errc = c.Events(sctx, 0)
	}

	tail, err := fetchLogs(ctx, c, service, n)
	if err != nil {
		return err
	}

	if !follow && a.json {
		if service != "" {
			return writeJSON(out, tail[0], true)
		}
		return writeJSON(out, allLogs{Services: tail}, true)
	}

	merged := mergeLogs(tail, n)
	seen := make(map[lineKey]struct{}, len(merged))
	var newest time.Time
	for _, l := range merged {
		seen[keyOf(l.service, l.line)] = struct{}{}
		if l.line.Time.After(newest) {
			newest = l.line.Time
		}
		if err := a.printLogLine(out, service == "", l); err != nil {
			return err
		}
	}
	if !follow {
		return nil
	}

	var lastID uint64
	idle := 0
	for {
		got := false
		for ev := range evs {
			got = true
			lastID = ev.ID
			if ev.Type != api.EventServiceLog {
				continue
			}
			var le api.ServiceLogEvent
			if json.Unmarshal(ev.Data, &le) != nil {
				continue
			}
			if service != "" && le.Service != service {
				continue
			}
			if le.Line.Time.Before(newest) {
				continue
			}
			if _, dup := seen[keyOf(le.Service, le.Line)]; dup {
				continue
			}
			if err := a.printLogLine(out, service == "", svcLine{service: le.Service, line: le.Line}); err != nil {
				return err
			}
		}
		serr := <-errc
		if ctx.Err() != nil {
			return nil // interrupted: the normal way to leave -f
		}
		if serr != nil {
			return fmt.Errorf("follow logs: %w", serr)
		}
		// The daemon ended the stream (it is shutting down, or we fell
		// behind). Resubscribe from the last event; if the daemon is gone
		// the dial reports it as unreachable.
		if got {
			idle = 0
		} else if idle++; idle >= maxFollowReconnects {
			return &ExitError{Code: ExitUnreachable,
				Err:  errors.New("the daemon keeps closing the log stream"),
				Hint: "Check the daemon log, then run the command again."}
		}
		evs, errc = c.Events(sctx, lastID)
	}
}

func (a *app) printLogLine(w io.Writer, showService bool, l svcLine) error {
	if a.json {
		return writeJSON(w, api.ServiceLogEvent{Service: l.service, Line: l.line}, false)
	}
	svc := ""
	if showService {
		svc = l.service
	}
	if _, err := fmt.Fprintln(w, formatLogLine(svc, l.line)); err != nil {
		return fmt.Errorf("write log output: %w", err)
	}
	return nil
}

// fetchLogs returns the tail of one service, or of every service when
// service is empty.
func fetchLogs(ctx context.Context, c *ipc.Client, service string, n int) ([]api.LogsResponse, error) {
	if service != "" {
		r, err := c.ServiceLogs(ctx, service, n)
		if err != nil {
			return nil, err
		}
		if r.Service == "" {
			r.Service = service
		}
		return []api.LogsResponse{r}, nil
	}
	svcs, err := c.Services(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.LogsResponse, 0, len(svcs))
	for _, s := range svcs {
		r, err := c.ServiceLogs(ctx, s.Name, n)
		if err != nil {
			return nil, err
		}
		if r.Service == "" {
			r.Service = s.Name
		}
		out = append(out, r)
	}
	return out, nil
}

// mergeLogs interleaves the services' lines by time and keeps the last n
// (all when n <= 0).
func mergeLogs(rs []api.LogsResponse, n int) []svcLine {
	var all []svcLine
	for _, r := range rs {
		for _, l := range r.Lines {
			all = append(all, svcLine{service: r.Service, line: l})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].line.Time.Before(all[j].line.Time) })
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
