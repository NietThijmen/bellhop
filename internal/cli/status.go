package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/pkg/version"
)

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the daemon, its services and any warnings",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			st, err := c.Status(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if a.json {
				return writeJSON(out, st, true)
			}
			return a.writeStatus(cmd, st)
		},
	}
}

func (a *app) writeStatus(cmd *cobra.Command, st api.Status) error {
	out := cmd.OutOrStdout()
	now := a.now()
	state := "running"
	if st.Degraded {
		state = "running (degraded)"
	}
	tw := newTable(out)
	_, _ = fmt.Fprintf(tw, "Daemon:\t%s, %sd %s (pid %d, up %s)\n", state, version.ID, dash(st.Build.Version),
		st.PID, humanDuration(now.Sub(st.StartedAt)))
	_, _ = fmt.Fprintf(tw, "Build:\tcommit %s, built %s, %s %s/%s\n",
		dash(st.Build.Commit), dash(st.Build.Date), dash(st.Build.Go), dash(st.Build.OS), dash(st.Build.Arch))
	_, _ = fmt.Fprintf(tw, "Data:\t%s\n", dash(st.DataDir))
	_, _ = fmt.Fprintf(tw, "Logs:\t%s\n", dash(st.LogDir))
	if err := tw.Flush(); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out)
	if err := writeServices(out, now, st.Services); err != nil {
		return err
	}
	if len(st.Warnings) > 0 {
		_, _ = fmt.Fprintf(out, "\nWarnings (%d):\n", len(st.Warnings))
		for _, w := range st.Warnings {
			_, _ = fmt.Fprintf(out, "  ! %s\n", w.Message)
			if w.Hint != "" {
				_, _ = fmt.Fprintf(out, "    %s\n", w.Hint)
			}
		}
	}
	return nil
}
