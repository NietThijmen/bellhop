//go:build windows

package platform

import (
	"fmt"
)

// BindCapSupported reports whether setcap applies on this OS.
func BindCapSupported() bool { return false }

// ApplyBindCap is a no-op on Windows. Port 80 and 443 need an elevated process.
func ApplyBindCap(string) error { return nil }

// ClearBindCap is a no-op on Windows.
func ClearBindCap(string) error { return nil }

// InstallResolver adds an NRPT rule for tld when it is not already present.
func InstallResolver(tld string) (ResolverChange, error) {
	if err := ValidTLD(tld); err != nil {
		return ResolverChange{}, err
	}
	out, err := CommandOutput("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", NRPTInstallScript(tld))
	if err != nil {
		return ResolverChange{}, fmt.Errorf("add NRPT rule for .%s: %w: %s", tld, err, out)
	}
	return ResolverChange{TLD: tld, Kind: ResolverNRPT}, nil
}

// RemoveResolver deletes the NRPT rule recorded for this TLD.
func RemoveResolver(change ResolverChange) error {
	if change.Kind != ResolverNRPT {
		return fmt.Errorf("cannot remove resolver kind %q on windows", change.Kind)
	}
	if err := ValidTLD(change.TLD); err != nil {
		return err
	}
	out, err := CommandOutput("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", NRPTRemoveScript(change.TLD))
	if err != nil {
		return fmt.Errorf("remove NRPT rule for .%s: %w: %s", change.TLD, err, out)
	}
	return nil
}
