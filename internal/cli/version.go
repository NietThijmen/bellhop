package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// versionProbeTimeout bounds how long `version` waits for the daemon.
const versionProbeTimeout = 3 * time.Second

// versionOutput is the --json output of `version`.
type versionOutput struct {
	Client      version.Info  `json:"client"`
	Daemon      *version.Info `json:"daemon,omitempty"`
	DaemonError string        `json:"daemonError,omitempty"`
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the client version and, if it is running, the daemon version",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := versionOutput{Client: version.Get()}
			c, err := a.client()
			if err == nil {
				ctx, cancel := context.WithTimeout(cmd.Context(), versionProbeTimeout)
				st, serr := c.Status(ctx)
				cancel()
				if serr == nil {
					out.Daemon = &st.Build
				} else {
					err = serr
				}
			}
			if err != nil {
				out.DaemonError = describe(err).msg
			}

			w := cmd.OutOrStdout()
			if a.json {
				return writeJSON(w, out, true)
			}
			tw := newTable(w)
			_, _ = fmt.Fprintf(tw, "%s:\t%s\n", version.ID, formatBuild(out.Client))
			if out.Daemon != nil {
				_, _ = fmt.Fprintf(tw, "%sd:\t%s\n", version.ID, formatBuild(*out.Daemon))
			} else {
				_, _ = fmt.Fprintf(tw, "%sd:\tnot reachable (%s)\n", version.ID, out.DaemonError)
			}
			return tw.Flush()
		},
	}
}

func formatBuild(i version.Info) string {
	return fmt.Sprintf("%s (commit %s, built %s, %s %s/%s)",
		dash(i.Version), dash(i.Commit), dash(i.Date), dash(i.Go), dash(i.OS), dash(i.Arch))
}
