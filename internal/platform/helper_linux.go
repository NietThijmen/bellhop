//go:build linux

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	systemdDropIn = "/etc/systemd/resolved.conf.d/bellhop.conf"
	nmDropIn      = "/etc/NetworkManager/dnsmasq.d/bellhop.conf"
)

// BindCapSupported reports whether setcap applies on this OS.
func BindCapSupported() bool { return true }

// ApplyBindCap grants cap_net_bind_service so bellhopd can bind port 80 and 443.
// Replacing the binary clears the capability, so this is re-run after each update.
func ApplyBindCap(binary string) error {
	if !filepath.IsAbs(binary) {
		return fmt.Errorf("setcap needs an absolute path to bellhopd")
	}
	args := LinuxSetcapArgs(binary)
	out, err := CommandOutput("setcap", args...)
	if err != nil {
		return fmt.Errorf("setcap %s: %w: %s", binary, err, out)
	}
	return nil
}

// ClearBindCap removes the file capability recorded for binary.
func ClearBindCap(binary string) error {
	if !filepath.IsAbs(binary) {
		return fmt.Errorf("setcap needs an absolute path to bellhopd")
	}
	out, err := CommandOutput("setcap", LinuxClearcapArgs(binary)...)
	if err != nil {
		return fmt.Errorf("setcap -r %s: %w: %s", binary, err, out)
	}
	return nil
}

// InstallResolver writes a systemd-resolved drop-in, or a NetworkManager dnsmasq rule.
func InstallResolver(tld string) (ResolverChange, error) {
	if err := ValidTLD(tld); err != nil {
		return ResolverChange{}, err
	}
	kind, err := linuxDNSKind()
	if err != nil {
		return ResolverChange{}, err
	}
	path, body, reload := linuxDNSFile(kind, tld)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // system config dir under /etc, read by non-root daemons
		return ResolverChange{}, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // read by systemd-resolved / dnsmasq running as their own users
		return ResolverChange{}, fmt.Errorf("write %s: %w", path, err)
	}
	out, err := CommandOutput(reload[0], reload[1:]...)
	if err != nil {
		return ResolverChange{}, fmt.Errorf("reload DNS after writing %s: %w: %s", path, err, out)
	}
	return ResolverChange{TLD: tld, Kind: kind, Path: path}, nil
}

// RemoveResolver deletes the drop-in this helper wrote and reloads the resolver.
func RemoveResolver(change ResolverChange) error {
	path, _, reload := linuxDNSFile(change.Kind, change.TLD)
	if reload == nil || change.Path != path {
		return fmt.Errorf("refusing to remove resolver kind %q path %s", change.Kind, change.Path)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	out, err := CommandOutput(reload[0], reload[1:]...)
	if err != nil {
		return fmt.Errorf("reload DNS after removing %s: %w: %s", path, err, out)
	}
	return nil
}

func linuxDNSKind() (string, error) {
	_, resolvectlErr := exec.LookPath("resolvectl")
	_, nmcliErr := exec.LookPath("nmcli")
	return SelectLinuxDNS(resolvectlErr == nil, nmcliErr == nil)
}

func linuxDNSFile(kind, tld string) (path, body string, reload []string) {
	switch kind {
	case ResolverSystemd:
		return systemdDropIn, SystemdResolvedDropIn(tld), []string{"systemctl", "reload", "systemd-resolved"}
	case ResolverNM:
		return nmDropIn, NetworkManagerDnsmasq(tld), []string{"systemctl", "reload", "NetworkManager"}
	default:
		return "", "", nil
	}
}
