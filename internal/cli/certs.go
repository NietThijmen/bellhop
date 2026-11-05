package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

func (a *app) certsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "certs",
		Short: "Show the local CA",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			out, err := c.Certs(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if a.json {
				return writeJSON(w, out, true)
			}
			tw := newTable(w)
			_, _ = fmt.Fprintf(tw, "CA:\t%s\n", out.CA)
			_, _ = fmt.Fprintf(tw, "Path:\t%s\n", out.Path)
			_, _ = fmt.Fprintf(tw, "Fingerprint:\t%s\n", out.Fingerprint)
			_, _ = fmt.Fprintf(tw, "Expires:\t%s\n", out.Expires.Format("2006-01-02"))
			trusted := "no"
			if out.Trusted {
				trusted = "yes"
			}
			_, _ = fmt.Fprintf(tw, "Trusted:\t%s\n", trusted)
			return tw.Flush()
		},
	}
	cmd.AddCommand(a.certsExportCmd())
	return cmd
}

func (a *app) certsExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export [file]",
		Short: "Print the local CA certificate, or copy it to a file",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			dest := ""
			if len(args) == 1 {
				dest, err = filepath.Abs(args[0])
				if err != nil {
					return err
				}
			}
			out, err := c.ExportCert(cmd.Context(), dest)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if a.json {
				return writeJSON(w, out, true)
			}
			if dest == "" {
				_, err = fmt.Fprint(w, out.PEM)
				return err
			}
			_, err = fmt.Fprintf(w, "wrote %s\n", out.Written)
			return err
		},
	}
}
