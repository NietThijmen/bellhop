// Command bellhop-helper performs the few operations that need elevated
// privileges: DNS resolver integration, the local CA trust store, and setcap.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/helper"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/pkg/version"
)

const (
	exitOK    = 0
	exitError = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintf(stderr, "%s-helper: %v\n", version.ID, err)
		return exitError
	}
	return exitOK
}

func newRoot() *cobra.Command {
	name := version.ID + "-helper"
	root := &cobra.Command{
		Use:   name,
		Short: "Privileged helper for " + version.Name,
		Long: "Privileged helper for " + version.Name + ". It is run by '" + version.ID +
			" setup' and '" + version.ID + " uninstall'; you should not need to run it yourself.",
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.AddCommand(versionCmd(name), dnsCmd(), trustCmd(true), trustCmd(false), setcapCmd(), undoCmd())
	return root
}

func versionCmd(name string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the helper's build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			i := version.Get()
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s (commit %s, built %s, %s %s/%s)\n",
				name, i.Version, i.Commit, i.Date, i.Go, i.OS, i.Arch)
			return err
		},
	}
}

func dnsCmd() *cobra.Command {
	var tld string
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Point the system resolver at the local TLD",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := dataDir()
			if err != nil {
				return err
			}
			st, err := helper.DNS(data, tld)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "dns %s via %s\n", st.DNS[0].TLD, st.DNS[0].Kind)
			return err
		},
	}
	cmd.Flags().StringVar(&tld, "tld", "", "top-level domain to resolve locally")
	_ = cmd.MarkFlagRequired("tld")
	return cmd
}

func trustCmd(install bool) *cobra.Command {
	use, short := "untrust", "Remove the local CA from the trust stores"
	if install {
		use, short = "trust", "Install the local CA into the trust stores"
	}
	var cert string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := dataDir()
			if err != nil {
				return err
			}
			var st helper.State
			if install {
				st, err = helper.Trust(data, cert)
			} else {
				st, err = helper.Untrust(data, cert)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s recorded %d certificate(s)\n", use, len(st.Trust))
			return err
		},
	}
	cmd.Flags().StringVar(&cert, "cert", "", "PEM certificate to trust or remove")
	_ = cmd.MarkFlagRequired("cert")
	return cmd
}

func setcapCmd() *cobra.Command {
	var binary string
	cmd := &cobra.Command{
		Use:   "setcap",
		Short: "Allow bellhopd to bind privileged ports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !platform.BindCapSupported() {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "setcap is only used on Linux")
				return err
			}
			data, err := dataDir()
			if err != nil {
				return err
			}
			if _, err := helper.Setcap(data, binary); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "setcap %s\n", binary)
			return err
		},
	}
	cmd.Flags().StringVar(&binary, "binary", "", "absolute path to bellhopd")
	_ = cmd.MarkFlagRequired("binary")
	return cmd
}

func undoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "undo",
		Short: "Reverse the DNS, trust and setcap changes this helper recorded",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := dataDir()
			if err != nil {
				return err
			}
			if _, err := helper.Undo(data); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "undo complete")
			return err
		},
	}
}

func dataDir() (string, error) {
	dirs, err := platform.Paths()
	if err != nil {
		return "", err
	}
	return dirs.Data, nil
}
