package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/pkg/version"
)

func (a *app) dbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "db",
		Short:   "Install and manage MariaDB",
		GroupID: groupDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(a.dbInstallCmd())
	return cmd
}

func (a *app) dbInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "install",
		Short:   "Download MariaDB, initialise its data directory and start it",
		Example: fmt.Sprintf("  %s db install", version.ID),
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ref, err := c.InstallDB(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !a.json {
				_, _ = fmt.Fprintf(out, "Installing MariaDB (job %s)...\n", ref.JobID)
			}
			res, err := a.waitJob(cmd, c, ref)
			if err != nil {
				return err
			}
			if a.json {
				return writeJSON(out, res, true)
			}
			_, _ = fmt.Fprintln(out, "MariaDB is installed.")
			return nil
		},
	}
}
