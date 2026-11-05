package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/artifacts"
	"github.com/nietthijmen/bellhop/internal/php"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/shim"
)

// runProcess replaces the process image's stdio with the PHP or Composer
// program. Tests stub it.
var runProcess = func(name string, args []string) (int, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

type silentExit struct{ code int }

func (e *silentExit) Error() string { return "" }

// passthroughArgs lets `bellhop php -v` and `bellhop composer install` reach
// the forwarder. Known php subcommands stay as they are.
func passthroughArgs(args []string) []string {
	if len(args) < 2 {
		return args
	}
	switch args[0] {
	case "php":
		switch args[1] {
		case "list", "ls", "install", "remove", "help", "-h", "--help", "--use", "--":
			return args
		default:
			return append([]string{"php", "--"}, args[1:]...)
		}
	case "composer":
		switch args[1] {
		case "-h", "--help", "--":
			return args
		default:
			return append([]string{"composer", "--"}, args[1:]...)
		}
	default:
		return args
	}
}

func (a *app) composerCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "composer [args...]",
		Short:   "Run Composer with the PHP version for the current site",
		GroupID: groupPHP,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.forwardTool(cmd, "", args, true)
		},
	}
}

func (a *app) forwardTool(cmd *cobra.Command, pin string, args []string, composer bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	listed, err := c.Sites(ctx)
	if err != nil {
		return err
	}
	def := php.DefaultVersion
	settings, err := c.Settings(ctx)
	if err == nil && settings[apiSettingDefaultPHP] != "" {
		def = settings[apiSettingDefaultPHP]
	} else if err != nil {
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Code != api.CodeNotFound {
			return err
		}
	}
	ver := pin
	if ver == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		sites := make([]shim.Site, len(listed.Sites))
		for i, s := range listed.Sites {
			sites[i] = shim.Site{Path: s.Path, PHP: s.PHP}
		}
		ver = shim.VersionFor(cwd, sites, def)
	}
	dirs, err := platform.Paths()
	if err != nil {
		return err
	}
	mgr, err := php.New(php.Options{
		DataDir:   dirs.Data,
		RunDir:    dirs.Run,
		Installer: &artifacts.Installer{DataDir: dirs.Data, DownloadDir: dirs.Downloads},
	})
	if err != nil {
		return err
	}
	if lines, err := mgr.Lines(); err == nil {
		if exe, err := os.Executable(); err == nil {
			if err := shim.Write(dirs.Bin, exe, lines); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "could not write PHP shims: %v\n", err)
			}
		}
	}
	bin, err := mgr.CLIBinary(ver)
	if err != nil {
		return err
	}
	argv := args
	if composer {
		phar, err := mgr.EnsureComposer(ctx)
		if err != nil {
			return err
		}
		argv = append([]string{phar}, args...)
	}
	code, err := runProcess(bin, argv)
	if err != nil {
		return err
	}
	if code != 0 {
		return &silentExit{code: code}
	}
	return nil
}

const apiSettingDefaultPHP = "default_php"
