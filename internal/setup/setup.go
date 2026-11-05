// Package setup is the ordered install and undo plan the CLI runs.
// Privileged work is one helper invocation. Tests replace the hooks.
package setup

import (
	"fmt"
	"strings"

	"github.com/nietthijmen/bellhop/internal/php"
)

// Request is one setup or uninstall.
type Request struct {
	TLD        string
	Localhost  bool
	CertPath   string // empty skips trust
	Binary     string // bellhopd, for setcap on Linux
	Helper     string // bellhop-helper path
	BinDir     string // <data>/bin, added to PATH
	PHPVersion string // empty means php.DefaultVersion
	Purge      bool
	Confirm    bool
}

// Hooks are the side effects. Nil hooks are skipped.
type Hooks struct {
	PortOwner        func(port int) (string, error)
	Elevate          func(helper string, args []string) error
	InstallAutostart func(binary string) error
	RemoveAutostart  func() error
	AddPath          func(dir string) error
	RemovePath       func(dir string) error
	InstallPHP       func(version string) error
	Databases        func() ([]string, error)
}

// ConflictError means another server owns a privileged port.
type ConflictError struct {
	Port  int
	Owner string
}

func (e *ConflictError) Error() string {
	owner := e.Owner
	if owner == "" {
		owner = "another program"
	}
	return fmt.Sprintf("port %d is in use by %s", e.Port, owner)
}

// Hint tells the user how to free the port.
func (e *ConflictError) Hint() string {
	switch strings.ToLower(e.Owner) {
	case "valet":
		return "run `valet stop`, then `bellhop setup` again"
	case "nginx":
		return "stop nginx, then `bellhop setup` again"
	case "httpd", "apache", "apache2":
		return "stop Apache, then `bellhop setup` again"
	case "w3wp", "iis", "iisexpress":
		return "stop the IIS site that uses this port, then `bellhop setup` again"
	default:
		return fmt.Sprintf("stop the program listening on port %d, then `bellhop setup` again", e.Port)
	}
}

// ApplyArgs is the one elevated helper invocation.
func ApplyArgs(req Request) []string {
	args := []string{"apply"}
	if !req.Localhost && req.TLD != "" {
		args = append(args, "--tld", req.TLD)
	}
	if req.CertPath != "" {
		args = append(args, "--cert", req.CertPath)
	}
	if req.Binary != "" {
		args = append(args, "--binary", req.Binary)
	}
	return args
}

// Setup checks ports, elevates once, installs autostart and PATH, then PHP.
func Setup(req Request, h Hooks) error {
	if req.PHPVersion == "" {
		req.PHPVersion = php.DefaultVersion
	}
	for _, port := range []int{80, 443} {
		if h.PortOwner == nil {
			break
		}
		owner, err := h.PortOwner(port)
		if err != nil {
			return err
		}
		if owner == "" || isBellhop(owner) {
			continue
		}
		return &ConflictError{Port: port, Owner: owner}
	}
	if h.Elevate != nil && req.Helper != "" {
		if err := h.Elevate(req.Helper, ApplyArgs(req)); err != nil {
			return err
		}
	}
	if h.InstallAutostart != nil && req.Binary != "" {
		if err := h.InstallAutostart(req.Binary); err != nil {
			return err
		}
	}
	if h.AddPath != nil && req.BinDir != "" {
		if err := h.AddPath(req.BinDir); err != nil {
			return err
		}
	}
	if h.InstallPHP != nil {
		if err := h.InstallPHP(req.PHPVersion); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall undoes the helper, autostart and PATH.
// Purge with databases refuses until Confirm is set, and returns the names.
func Uninstall(req Request, h Hooks) ([]string, error) {
	var names []string
	if req.Purge && h.Databases != nil {
		var err error
		names, err = h.Databases()
		if err != nil {
			return nil, err
		}
		if len(names) > 0 && !req.Confirm {
			return names, fmt.Errorf("purge would drop %s; pass --confirm to continue", strings.Join(names, ", "))
		}
	}
	if h.Elevate != nil && req.Helper != "" {
		if err := h.Elevate(req.Helper, []string{"undo"}); err != nil {
			return names, err
		}
	}
	if h.RemoveAutostart != nil {
		if err := h.RemoveAutostart(); err != nil {
			return names, err
		}
	}
	if h.RemovePath != nil && req.BinDir != "" {
		if err := h.RemovePath(req.BinDir); err != nil {
			return names, err
		}
	}
	return names, nil
}

// Check is one doctor row.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// DoctorInput is what the running daemon and helper state can tell us.
type DoctorInput struct {
	DaemonErr      error
	HTTPPort       int
	HTTPSPort      int
	Degraded       bool
	Localhost      bool
	DNSRecorded    bool
	Trusted        bool
	CertErr        error
	SetcapNeeded   bool
	SetcapRecorded bool
}

// Doctor reports whether uninstall would be starting from a clean setup.
func Doctor(in DoctorInput) []Check {
	checks := []Check{{
		Name: "daemon", OK: in.DaemonErr == nil,
		Detail: detailErr(in.DaemonErr, "running"),
	}}
	ports := Check{Name: "ports", OK: in.HTTPPort > 0 && in.HTTPSPort > 0, Detail: fmt.Sprintf("http %d, https %d", in.HTTPPort, in.HTTPSPort)}
	if in.Degraded {
		ports.Detail += " (fallback; 80 or 443 is unavailable)"
	}
	checks = append(checks, ports)
	dns := Check{Name: "dns", OK: in.Localhost || in.DNSRecorded}
	if in.Localhost {
		dns.Detail = "localhost mode"
	} else if in.DNSRecorded {
		dns.Detail = "helper recorded a resolver rule"
	} else {
		dns.Detail = "no resolver rule recorded; run setup"
	}
	checks = append(checks, dns)
	trust := Check{Name: "trust", OK: in.Trusted && in.CertErr == nil, Detail: "local CA is trusted"}
	if in.CertErr != nil {
		trust.Detail = in.CertErr.Error()
	} else if !in.Trusted {
		trust.Detail = "local CA is not in the trust store"
	}
	checks = append(checks, trust)
	setcap := Check{Name: "setcap", OK: !in.SetcapNeeded || in.SetcapRecorded, Detail: "not used on this system"}
	if in.SetcapNeeded {
		if in.SetcapRecorded {
			setcap.Detail = "cap_net_bind_service recorded"
		} else {
			setcap.Detail = "cap_net_bind_service is not recorded"
		}
	}
	checks = append(checks, setcap)
	return checks
}

func detailErr(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

func isBellhop(owner string) bool {
	base := strings.ToLower(owner)
	base = strings.TrimSuffix(base, ".exe")
	return base == "bellhopd" || base == "bellhop"
}
