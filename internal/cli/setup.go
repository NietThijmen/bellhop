package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/helper"
	"github.com/nietthijmen/bellhop/internal/php"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/setup"
	"github.com/nietthijmen/bellhop/pkg/version"
)

func (a *app) setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install DNS, trust, autostart, PATH and the default PHP",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSetup(cmd, false, false, false)
		},
	}
}

func (a *app) uninstallCmd() *cobra.Command {
	var purge, confirm bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Undo DNS, trust, autostart and PATH",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSetup(cmd, true, purge, confirm)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also drop databases after confirmation")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "confirm dropping databases listed by --purge")
	return cmd
}

func (a *app) tldCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tld <name>",
		Short: "Change the local TLD and refresh DNS and certificates",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if _, err := c.PatchSettings(cmd.Context(), map[string]string{"tld": args[0]}); err != nil {
				return err
			}
			helperPath, err := helperBinary()
			if err != nil {
				return err
			}
			if err := platform.Elevate(helperPath, []string{"dns", "--tld", args[0]}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "tld is %s\n", args[0])
			return err
		},
	}
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the daemon, ports, DNS, trust and setcap",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in := setup.DoctorInput{SetcapNeeded: platform.BindCapSupported()}
			c, err := a.client()
			if err != nil {
				in.DaemonErr = err
			} else if st, err := c.Status(cmd.Context()); err != nil {
				in.DaemonErr = err
			} else {
				in.Degraded = st.Degraded
				in.HTTPPort = 80
				in.HTTPSPort = 443
				for _, w := range st.Warnings {
					if w.Code == "proxy_ports" {
						in.Degraded = true
						in.HTTPPort = 8080
						in.HTTPSPort = 8443
					}
					if w.Code == "proxy_start" {
						in.HTTPPort = 0
						in.HTTPSPort = 0
					}
				}
			}
			if c != nil && in.DaemonErr == nil {
				if certs, err := c.Certs(cmd.Context()); err != nil {
					in.CertErr = err
				} else {
					in.Trusted = certs.Trusted
				}
				if settings, err := c.Settings(cmd.Context()); err == nil {
					in.Localhost = settings["localhost_mode"] == "true"
				}
			}
			if dirs, err := platform.Paths(); err == nil {
				st, err := helper.Load(dirs.Data)
				if err == nil {
					in.DNSRecorded = len(st.DNS) > 0
					in.SetcapRecorded = len(st.Setcap) > 0
				}
			}
			checks := setup.Doctor(in)
			if a.json {
				return writeJSON(cmd.OutOrStdout(), checks, true)
			}
			tw := newTable(cmd.OutOrStdout())
			ok := true
			for _, check := range checks {
				state := "ok"
				if !check.OK {
					state = "fail"
					ok = false
				}
				_, _ = fmt.Fprintf(tw, "%s:\t%s\t%s\n", check.Name, state, check.Detail)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("doctor found a problem")
			}
			return nil
		},
	}
}

func (a *app) runSetup(cmd *cobra.Command, uninstall, purge, confirm bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	settings, err := c.Settings(cmd.Context())
	if err != nil {
		return err
	}
	dirs, err := platform.Paths()
	if err != nil {
		return err
	}
	helperPath, err := helperBinary()
	if err != nil {
		return err
	}
	req := setup.Request{
		TLD:       settings["tld"],
		Localhost: settings["localhost_mode"] == "true",
		Helper:    helperPath,
		BinDir:    dirs.Bin,
		Binary:    daemonBinary(),
		Purge:     purge,
		Confirm:   confirm,
	}
	if certs, err := c.Certs(cmd.Context()); err == nil {
		req.CertPath = certs.Path
	}
	hooks := setup.Hooks{
		PortOwner:        platform.PortOwner,
		Elevate:          platform.Elevate,
		InstallAutostart: platform.InstallAutostart,
		RemoveAutostart:  platform.RemoveAutostart,
		AddPath:          platform.EnsureUserPath,
		RemovePath:       platform.RemoveUserPath,
		InstallPHP: func(version string) error {
			ref, err := c.InstallPHP(cmd.Context(), version)
			if err != nil {
				return err
			}
			_, err = a.waitJob(cmd, c, ref)
			return err
		},
	}
	if uninstall {
		names, err := setup.Uninstall(req, hooks)
		if err != nil {
			if len(names) > 0 {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "databases: %s\n", names)
			}
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "uninstalled")
		return err
	}
	if err := setup.Setup(req, hooks); err != nil {
		if conflict, ok := err.(*setup.ConflictError); ok {
			return fmt.Errorf("%s\n%s", conflict.Error(), conflict.Hint())
		}
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "setup complete (PHP %s)\n", php.DefaultVersion)
	return err
}

func helperBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), version.ID+"-helper"+platform.ExeSuffix()), nil
}

func daemonBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), version.ID+"d"+platform.ExeSuffix())
}
