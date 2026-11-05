package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// serviceActionCmd builds start, stop and restart.
func (a *app) serviceActionCmd(action, past string) *cobra.Command {
	return &cobra.Command{
		Use:     action + " [service]",
		Short:   fmt.Sprintf("%s one service, or all services when none is given", strings.ToUpper(action[:1])+action[1:]),
		Example: fmt.Sprintf("  %[1]s %[2]s\n  %[1]s %[2]s mariadb", version.ID, action),
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := api.ServiceAll
			if len(args) == 1 {
				name = args[0]
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if err := c.ServiceAction(ctx, name, action); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			svcs, serr := c.Services(ctx)
			if a.json {
				if serr != nil {
					return serr
				}
				return writeJSON(out, api.ServicesResponse{Services: filterServices(svcs, name)}, true)
			}
			if name == api.ServiceAll {
				_, _ = fmt.Fprintf(out, "%s all services.\n", past)
			} else {
				_, _ = fmt.Fprintf(out, "%s %s.\n", past, name)
			}
			if serr != nil {
				// The action itself succeeded; the table is a courtesy.
				return nil
			}
			_, _ = fmt.Fprintln(out)
			return writeServices(out, a.now(), filterServices(svcs, name))
		},
	}
}

// filterServices returns only the named service, or all for api.ServiceAll.
func filterServices(svcs []api.Service, name string) []api.Service {
	if name == api.ServiceAll {
		if svcs == nil {
			return []api.Service{}
		}
		return svcs
	}
	out := []api.Service{}
	for _, s := range svcs {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}
