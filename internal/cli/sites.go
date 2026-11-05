package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// unlinkOutput is the --json output of `unlink`.
type unlinkOutput struct {
	Unlinked string `json:"unlinked"`
}

// absArg returns the absolute form of a path argument, or of the current
// directory when the argument is absent. The daemon only accepts absolute
// paths.
func absArg(args []string) (string, error) {
	p := "."
	if len(args) == 1 && args[0] != "" {
		p = args[0]
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", p, err)
	}
	return abs, nil
}

// siteAt returns the site whose directory is dir or the closest ancestor of
// dir, considering only sites for which keep returns true (nil keeps all).
//
// Paths are compared by file identity (os.SameFile) after an exact textual
// match, so the comparison follows the filesystem: case-insensitive where
// the volume is (Windows, default APFS), case-sensitive elsewhere, and
// independent of symlinks and 8.3 short names.
func siteAt(sites []api.Site, dir string, keep func(api.Site) bool) (api.Site, bool) {
	type candidate struct {
		site api.Site
		fi   os.FileInfo
	}
	var kept []api.Site
	var cands []candidate
	for _, s := range sites {
		if keep != nil && !keep(s) {
			continue
		}
		kept = append(kept, s)
		if fi, err := os.Stat(s.Path); err == nil {
			cands = append(cands, candidate{s, fi})
		}
	}
	for d := filepath.Clean(dir); ; {
		for _, s := range kept {
			if s.Path != "" && filepath.Clean(s.Path) == d {
				return s, true
			}
		}
		if fi, err := os.Stat(d); err == nil {
			for _, c := range cands {
				if os.SameFile(fi, c.fi) {
					return c.site, true
				}
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return api.Site{}, false
		}
		d = parent
	}
}

func isLinked(s api.Site) bool { return s.Source == api.SourceLinked }

// currentSite finds the site containing the working directory.
func currentSite(cmd *cobra.Command, c *ipc.Client, keep func(api.Site) bool, what string) (api.Site, error) {
	cwd, err := absArg(nil)
	if err != nil {
		return api.Site{}, err
	}
	r, err := c.Sites(cmd.Context())
	if err != nil {
		return api.Site{}, err
	}
	if s, ok := siteAt(r.Sites, cwd, keep); ok {
		return s, nil
	}
	return api.Site{}, &api.Error{
		Code:    api.CodeNotFound,
		Message: fmt.Sprintf("%s is not inside any %s", cwd, what),
		Hint:    fmt.Sprintf("Pass a site name, or run this inside the site's folder ('%s sites' lists them).", version.ID),
	}
}

// siteName returns args[0] (or flagName) when given, else the name of the
// site containing the working directory.
func siteName(cmd *cobra.Command, c *ipc.Client, given string) (string, error) {
	if given != "" {
		return given, nil
	}
	s, err := currentSite(cmd, c, nil, "site")
	return s.Name, err
}

func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// ---------------------------------------------------------------- park

func (a *app) parkCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "park [path]",
		Short:   "Serve every folder inside a directory as a site (default: current directory)",
		Example: fmt.Sprintf("  %[1]s park\n  %[1]s park ~/Sites", version.ID),
		GroupID: groupSites,
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.parkAction(cmd, args, true)
		},
	}
}

func (a *app) forgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "forget [path]",
		Short:   "Stop serving a parked directory (default: current directory)",
		GroupID: groupSites,
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.parkAction(cmd, args, false)
		},
	}
}

func (a *app) parkAction(cmd *cobra.Command, args []string, park bool) error {
	path, err := absArg(args)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if park {
		err = c.Park(ctx, path)
	} else {
		err = c.Unpark(ctx, path)
	}
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if a.json {
		paths, err := c.Parked(ctx)
		if err != nil {
			return err
		}
		return writeJSON(out, api.ParkedResponse{Paths: nonNil(paths)}, true)
	}
	if park {
		_, _ = fmt.Fprintf(out, "Parked %s.\nEvery folder in it is now served as a site; run '%s sites' to see their URLs.\n", path, version.ID)
	} else {
		_, _ = fmt.Fprintf(out, "Forgot %s; its folders are no longer served.\n", path)
	}
	return nil
}

func (a *app) pathsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "paths",
		Short:   "List parked directories",
		GroupID: groupSites,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			paths, err := c.Parked(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if a.json {
				return writeJSON(out, api.ParkedResponse{Paths: nonNil(paths)}, true)
			}
			if len(paths) == 0 {
				_, _ = fmt.Fprintf(out, "No parked directories. Run '%s park' inside a folder of projects.\n", version.ID)
				return nil
			}
			for _, p := range paths {
				_, _ = fmt.Fprintln(out, p)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------- link

func (a *app) linkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link [name]",
		Short: "Serve the current directory as a site (default name: the folder name)",
		Long: "Serve the current directory as a single site. Without a name the site is\n" +
			"named after the folder, lowercased and reduced to letters, digits and dashes.",
		Example: fmt.Sprintf("  %[1]s link\n  %[1]s link shop", version.ID),
		GroupID: groupSites,
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := absArg(nil)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			s, err := c.Link(cmd.Context(), api.LinkRequest{Path: path, Name: firstArg(args)})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if a.json {
				return writeJSON(out, s, true)
			}
			_, _ = fmt.Fprintf(out, "Linked %s as %s: %s\n", path, s.Name, s.URL)
			return nil
		},
	}
}

func (a *app) unlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unlink [name]",
		Short:   "Stop serving a linked site (default: the linked site in the current directory)",
		GroupID: groupSites,
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			name := firstArg(args)
			if name == "" {
				s, err := currentSite(cmd, c, isLinked, "linked site")
				if err != nil {
					return err
				}
				name = s.Name
			}
			if err := c.Unlink(cmd.Context(), name); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if a.json {
				return writeJSON(out, unlinkOutput{Unlinked: name}, true)
			}
			_, _ = fmt.Fprintf(out, "Unlinked %s.\n", name)
			return nil
		},
	}
}

// ---------------------------------------------------------------- listing

func (a *app) sitesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "sites",
		Short:   "List every served site with its URL, driver and PHP version",
		GroupID: groupSites,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.listSites(cmd, nil, fmt.Sprintf(
				"No sites. Park a folder of projects with '%[1]s park' or link one with '%[1]s link'.", version.ID))
		},
	}
}

func (a *app) linksCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "links",
		Short:   "List linked sites",
		GroupID: groupSites,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.listSites(cmd, isLinked, fmt.Sprintf(
				"No linked sites. Run '%s link' inside a project folder.", version.ID))
		},
	}
}

func (a *app) listSites(cmd *cobra.Command, keep func(api.Site) bool, empty string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.Sites(cmd.Context())
	if err != nil {
		return err
	}
	sites := []api.Site{}
	for _, s := range r.Sites {
		if keep == nil || keep(s) {
			sites = append(sites, s)
		}
	}
	out := cmd.OutOrStdout()
	if keep != nil {
		r.Conflicts = nil // conflicts are about parked roots
	}
	if a.json {
		return writeJSON(out, api.SitesResponse{Sites: sites, Conflicts: r.Conflicts}, true)
	}
	writeWarnings(cmd.ErrOrStderr(), r.Conflicts)
	if len(sites) == 0 {
		_, _ = fmt.Fprintln(out, empty)
		return nil
	}
	return writeSites(out, sites)
}

// writeSites prints sites as an aligned table.
func writeSites(w io.Writer, sites []api.Site) error {
	tw := newTable(w)
	_, _ = fmt.Fprintln(tw, "NAME\tURL\tDRIVER\tPHP\tSECURE\tPATH")
	for _, s := range sites {
		php := dash(s.PHP)
		if s.Isolated && s.PHP != "" {
			php += " (isolated)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Name, dash(s.URL), dash(s.Driver), php, yesNo(s.Secure), s.Path)
	}
	return tw.Flush()
}

// writeWarnings prints warnings to stderr-style output.
func writeWarnings(w io.Writer, ws []api.Warning) {
	for _, x := range ws {
		_, _ = fmt.Fprintf(w, "warning: %s\n", x.Message)
		if x.Hint != "" {
			_, _ = fmt.Fprintf(w, "  %s\n", x.Hint)
		}
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ---------------------------------------------------------------- open

func (a *app) openURL(raw string) error {
	return platform.OpenURL(raw)
}

func (a *app) openCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "open [name]",
		Short:   "Open a site in the default browser (default: the site in the current directory)",
		GroupID: groupSites,
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var s api.Site
			if name := firstArg(args); name != "" {
				r, err := c.Sites(cmd.Context())
				if err != nil {
					return err
				}
				found := false
				for _, x := range r.Sites {
					if x.Name == name {
						s, found = x, true
						break
					}
				}
				if !found {
					return &api.Error{Code: api.CodeNotFound,
						Message: fmt.Sprintf("there is no site named %q", name),
						Hint:    fmt.Sprintf("Run '%s sites' to list them.", version.ID)}
				}
			} else if s, err = currentSite(cmd, c, nil, "site"); err != nil {
				return err
			}
			if s.URL == "" {
				return fmt.Errorf("site %s has no URL yet (check '%s status' for proxy problems)", s.Name, version.ID)
			}
			if err := a.openURL(s.URL); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if a.json {
				return writeJSON(out, s, true)
			}
			_, _ = fmt.Fprintf(out, "Opened %s\n", s.URL)
			return nil
		},
	}
}

// ---------------------------------------------------------------- secure

func (a *app) secureCmd(secure bool) *cobra.Command {
	use, short := "secure [name]", "Serve a site over HTTPS (default: the site in the current directory)"
	if !secure {
		use, short = "unsecure [name]", "Serve a site over plain HTTP (default: the site in the current directory)"
	}
	return &cobra.Command{
		Use:     use,
		Short:   short,
		GroupID: groupSites,
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.patchSite(cmd, firstArg(args), api.SitePatch{Secure: &secure})
			if err != nil {
				return err
			}
			if a.json {
				return writeJSON(cmd.OutOrStdout(), s, true)
			}
			out := cmd.OutOrStdout()
			if secure {
				_, _ = fmt.Fprintf(out, "Secured %s: %s\n", s.Name, s.URL)
				if strings.HasPrefix(s.URL, "http://") {
					_, _ = fmt.Fprintf(out, "Note: HTTPS is not active yet, so the site is still served over HTTP. "+
						"It switches to HTTPS once '%s setup' has installed DNS and the local certificate authority.\n", version.ID)
				}
			} else {
				_, _ = fmt.Fprintf(out, "Unsecured %s: %s\n", s.Name, s.URL)
			}
			return nil
		},
	}
}

// patchSite resolves the site name (default: the site at cwd) and patches it.
func (a *app) patchSite(cmd *cobra.Command, name string, p api.SitePatch) (api.Site, error) {
	c, err := a.client()
	if err != nil {
		return api.Site{}, err
	}
	name, err = siteName(cmd, c, name)
	if err != nil {
		return api.Site{}, err
	}
	return c.PatchSite(cmd.Context(), name, p)
}
