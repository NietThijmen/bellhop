// Command bellhop-helper performs the few operations that need elevated
// privileges (DNS resolver integration, trust store, setcap). Milestone M1
// ships only the skeleton: the privileged operations arrive in M4.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the helper with args and returns the exit code.
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
		// Note: the dns, trust, untrust, setcap and undo operations arrive
		// in milestone M4. Until then every operation other than version is
		// rejected with exit code 1.
		Long: "Privileged helper for " + version.Name + ". It is run by '" + version.ID +
			" setup' and '" + version.ID + " uninstall'; you should not need to run it yourself.",
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown operation %q: this build of %s only supports 'version' "+
				"(DNS, trust and setcap operations are not available yet); update %s and run '%s setup' again",
				args[0], name, version.Name, version.ID)
		},
	}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the helper's build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			i := version.Get()
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s (commit %s, built %s, %s %s/%s)\n",
				name, i.Version, i.Commit, i.Date, i.Go, i.OS, i.Arch)
			return err
		},
	})
	return root
}
