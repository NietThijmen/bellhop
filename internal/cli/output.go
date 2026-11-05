package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
)

// newTable returns a tabwriter for aligned human output. Call Flush.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// dash renders empty values as "-" so table columns never collapse.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func intOrDash(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

// humanDuration renders d compactly: 45s, 4m12s, 2h5m, 3d4h.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// writeServices prints services as an aligned table.
func writeServices(w io.Writer, now time.Time, svcs []api.Service) error {
	if len(svcs) == 0 {
		_, err := fmt.Fprintln(w, "No services configured.")
		return err
	}
	tw := newTable(w)
	_, _ = fmt.Fprintln(tw, "SERVICE\tSTATE\tPID\tPORT\tRESTARTS\tFOR\tLAST ERROR")
	for _, s := range svcs {
		since := "-"
		if !s.Since.IsZero() {
			since = humanDuration(now.Sub(s.Since))
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			s.Name, s.State, intOrDash(s.PID), intOrDash(s.Port), s.Restarts, since, dash(oneLine(s.LastError)))
	}
	return tw.Flush()
}

// oneLine keeps table cells on one line.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] + " ..."
	}
	return s
}

// formatLogLine renders one log line. service is omitted when empty.
func formatLogLine(service string, l api.LogLine) string {
	ts := l.Time.Local().Format("2006-01-02 15:04:05.000")
	if service != "" {
		return fmt.Sprintf("%s %s %-6s %s", ts, service, l.Stream, l.Line)
	}
	return fmt.Sprintf("%s %-6s %s", ts, l.Stream, l.Line)
}
