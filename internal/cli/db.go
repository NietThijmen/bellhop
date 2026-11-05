package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
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
	cmd.AddCommand(a.dbInstallCmd(), a.dbProvisionCmd())
	return cmd
}

func (a *app) dbProvisionCmd() *cobra.Command {
	var site string
	var write bool
	var database, username, password string
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Create the site database when it points at this MariaDB",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			name, err := siteName(cmd, c, site)
			if err != nil {
				return err
			}
			req := api.DBProvisionRequest{Database: database, Username: username, Password: password}
			preview, err := c.ProvisionDB(cmd.Context(), name, req)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if preview.Diff != "" {
				_, _ = fmt.Fprintf(out, "dotenv changes:\n%s", preview.Diff)
			}
			if preview.Reason != "" {
				_, _ = fmt.Fprintln(out, preview.Reason)
				return nil
			}
			if !write {
				if preview.Diff != "" {
					_, _ = fmt.Fprintln(out, "re-run with --write to apply this")
				}
				return nil
			}
			req.Write = true
			res, err := c.ProvisionDB(cmd.Context(), name, req)
			if err != nil {
				return err
			}
			if res.Reason != "" {
				_, _ = fmt.Fprintln(out, res.Reason)
				return nil
			}
			_, _ = fmt.Fprintf(out, "provisioned %s\n", res.Database)
			return nil
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "site name (default: the site in the current directory)")
	cmd.Flags().BoolVar(&write, "write", false, "write missing DB_* keys to .env and create the database")
	cmd.Flags().StringVar(&database, "database", "", "database name when the project has no DB settings")
	cmd.Flags().StringVar(&username, "username", "", "database user when the project has no DB settings")
	cmd.Flags().StringVar(&password, "password", "", "database password when the project has no DB settings")
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
