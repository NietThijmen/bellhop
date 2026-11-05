package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// exactArgs is cobra.ExactArgs with a usage error.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return &usageError{cmd: cmd.CommandPath(),
				err: fmt.Errorf("%s needs exactly %d argument(s), got %d", cmd.CommandPath(), n, len(args))}
		}
		return nil
	}
}

func (a *app) isolateCmd() *cobra.Command {
	var site string
	cmd := &cobra.Command{
		Use:     "isolate <version>",
		Short:   "Pin a site to a PHP version (default: the site in the current directory)",
		Example: fmt.Sprintf("  %[1]s isolate 8.2\n  %[1]s isolate 8.3 --site shop", version.ID),
		GroupID: groupPHP,
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v := args[0]
			s, err := a.patchSite(cmd, site, api.SitePatch{PHP: &v})
			if err != nil {
				return err
			}
			if a.json {
				return writeJSON(cmd.OutOrStdout(), s, true)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s now uses PHP %s.\n", s.Name, dash(s.PHP))
			return nil
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "site name (default: the site in the current directory)")
	return cmd
}

func (a *app) unisolateCmd() *cobra.Command {
	var site string
	cmd := &cobra.Command{
		Use:     "unisolate",
		Short:   "Return a site to the default PHP version (default: the site in the current directory)",
		GroupID: groupPHP,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reset := ""
			s, err := a.patchSite(cmd, site, api.SitePatch{PHP: &reset})
			if err != nil {
				return err
			}
			if a.json {
				return writeJSON(cmd.OutOrStdout(), s, true)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s now uses the default PHP version (%s).\n", s.Name, dash(s.PHP))
			return nil
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "site name (default: the site in the current directory)")
	return cmd
}

func (a *app) useCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "use <version>",
		Short:   "Set the default PHP version for every site that is not isolated",
		Example: fmt.Sprintf("  %s use 8.3", version.ID),
		GroupID: groupPHP,
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if err := c.SetDefaultPHP(ctx, args[0]); err != nil {
				return err
			}
			if a.json {
				vs, err := c.PHPVersions(ctx)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), api.PHPResponse{Versions: nonNil(vs)}, true)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Default PHP version is now %s.\n", args[0])
			return nil
		},
	}
}

func (a *app) phpCmd() *cobra.Command {
	var pin string
	cmd := &cobra.Command{
		Use:     "php [args...]",
		Short:   "Run PHP, or list, install and remove versions",
		GroupID: groupPHP,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && pin == "" {
				return cmd.Help()
			}
			return a.forwardTool(cmd, pin, args, false)
		},
	}
	cmd.Flags().StringVar(&pin, "use", "", "PHP line to run instead of the site in the current directory")
	cmd.AddCommand(a.phpListCmd(), a.phpInstallCmd(), a.phpRemoveCmd())
	return cmd
}

func (a *app) phpListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List PHP versions, installed and available",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			vs, err := c.PHPVersions(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if a.json {
				return writeJSON(out, api.PHPResponse{Versions: nonNil(vs)}, true)
			}
			return writePHPVersions(out, vs)
		},
	}
}

// writePHPVersions prints PHP versions as an aligned table.
func writePHPVersions(w io.Writer, vs []api.PHPVersion) error {
	if len(vs) == 0 {
		_, err := fmt.Fprintln(w, "No PHP versions are known to the daemon.")
		return err
	}
	tw := newTable(w)
	_, _ = fmt.Fprintln(tw, "VERSION\tPATCH\tINSTALLED\tDEFAULT\tSITES\tRUNNING\tPATH")
	for _, v := range vs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			v.Version, dash(v.Patch), yesNo(v.Installed), yesNo(v.Default), v.InUse, yesNo(v.Running), dash(v.Path))
	}
	return tw.Flush()
}

func (a *app) phpInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "install <version>",
		Short:   "Download and install a PHP version, waiting for it to finish",
		Example: fmt.Sprintf("  %s php install 8.3", version.ID),
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ref, err := c.InstallPHP(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !a.json {
				_, _ = fmt.Fprintf(out, "Installing PHP %s (job %s)...\n", args[0], ref.JobID)
			}
			if _, err := a.waitJob(cmd, c, ref); err != nil {
				return err
			}
			if !a.json {
				_, _ = fmt.Fprintf(out, "Installed PHP %s.\n", args[0])
			}
			return nil
		},
	}
}

func (a *app) phpRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <version>",
		Aliases: []string{"rm", "uninstall"},
		Short:   "Remove an installed PHP version",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if err := c.RemovePHP(ctx, args[0]); err != nil {
				return err
			}
			if a.json {
				vs, err := c.PHPVersions(ctx)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), api.PHPResponse{Versions: nonNil(vs)}, true)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed PHP %s.\n", args[0])
			return nil
		},
	}
}
