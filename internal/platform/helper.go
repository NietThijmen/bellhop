package platform

import (
	"fmt"
	"unicode"
)

// Resolver kinds recorded in helper-state.json.
const (
	ResolverNRPT    = "nrpt"
	ResolverMacOS   = "macos-resolver"
	ResolverSystemd = "systemd-resolved"
	ResolverNM      = "networkmanager"
	NRPTComment     = "bellhop"
	DNSNameserver   = "127.0.0.1"
	DNSUnprivileged = "5354"
)

// ErrNoDNSIntegration means Linux has neither systemd-resolved nor NetworkManager.
var ErrNoDNSIntegration = fmt.Errorf("no systemd-resolved or NetworkManager found; use localhost mode instead of a local TLD")

// ResolverChange is one DNS integration the helper installed.
type ResolverChange struct {
	TLD  string
	Kind string
	Path string
}

// ValidTLD rejects values that cannot be a single DNS label.
func ValidTLD(tld string) error {
	if tld == "" || len(tld) > 63 {
		return fmt.Errorf("invalid tld %q", tld)
	}
	for i, r := range tld {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(tld)-1:
		default:
			if unicode.IsSpace(r) || r == '/' || r == '\\' || r == '.' {
				return fmt.Errorf("invalid tld %q", tld)
			}
			return fmt.Errorf("invalid tld %q", tld)
		}
	}
	return nil
}

// NRPTInstallScript adds a Windows NRPT rule when one with this namespace
// and comment is not already present.
func NRPTInstallScript(tld string) string {
	return fmt.Sprintf(`$ns = '.%s'
$rules = @(Get-DnsClientNrptRule -ErrorAction SilentlyContinue | Where-Object { $_.Comment -eq '%s' -and $_.Namespace -eq $ns })
if ($rules.Count -eq 0) {
  Add-DnsClientNrptRule -Namespace $ns -NameServers '%s' -Comment '%s' | Out-Null
}
`, tld, NRPTComment, DNSNameserver, NRPTComment)
}

// NRPTRemoveScript removes NRPT rules for tld that this helper added.
func NRPTRemoveScript(tld string) string {
	return fmt.Sprintf(`Get-DnsClientNrptRule -ErrorAction SilentlyContinue | Where-Object { $_.Comment -eq '%s' -and $_.Namespace -eq '.%s' } | ForEach-Object { Remove-DnsClientNrptRule -Name $_.Name -Force }
`, NRPTComment, tld)
}

// MacResolverFile is the body of /etc/resolver/<tld>.
func MacResolverFile() string {
	return "nameserver " + DNSNameserver + "\nport " + DNSUnprivileged + "\n"
}

// SystemdResolvedDropIn routes one TLD to the local resolver.
func SystemdResolvedDropIn(tld string) string {
	return "[Resolve]\nDNS=" + DNSNameserver + ":" + DNSUnprivileged + "\nDomains=~" + tld + "\n"
}

// NetworkManagerDnsmasq is a dnsmasq server= rule for NetworkManager.
func NetworkManagerDnsmasq(tld string) string {
	return "server=/" + tld + "/" + DNSNameserver + "#" + DNSUnprivileged + "\n"
}

// SelectLinuxDNS prefers systemd-resolved and otherwise NetworkManager.
func SelectLinuxDNS(resolved, networkManager bool) (string, error) {
	switch {
	case resolved:
		return ResolverSystemd, nil
	case networkManager:
		return ResolverNM, nil
	default:
		return "", ErrNoDNSIntegration
	}
}

// LinuxSetcapArgs is the setcap invocation that lets bellhopd bind port 80 and 443.
func LinuxSetcapArgs(binary string) []string {
	return []string{"cap_net_bind_service=+ep", binary}
}

// LinuxClearcapArgs removes file capabilities from binary.
func LinuxClearcapArgs(binary string) []string {
	return []string{"-r", binary}
}
