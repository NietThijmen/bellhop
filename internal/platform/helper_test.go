package platform

import (
	"errors"
	"strings"
	"testing"
)

func TestHelperDNSText(t *testing.T) {
	if err := ValidTLD("test"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "test.com", "../etc", "te st", "-test", "test-"} {
		if err := ValidTLD(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}

	install := NRPTInstallScript("test")
	for _, want := range []string{
		"Add-DnsClientNrptRule",
		"-Namespace $ns",
		"-NameServers '127.0.0.1'",
		"-Comment 'bellhop'",
		"$ns = '.test'",
		"$_.Comment -eq 'bellhop'",
	} {
		if !strings.Contains(install, want) {
			t.Fatalf("install script missing %q:\n%s", want, install)
		}
	}
	remove := NRPTRemoveScript("test")
	if !strings.Contains(remove, "Remove-DnsClientNrptRule -Name $_.Name -Force") || !strings.Contains(remove, ".test") {
		t.Fatalf("remove script:\n%s", remove)
	}
	if mac := MacResolverFile(); mac != "nameserver 127.0.0.1\nport 5354\n" {
		t.Fatalf("mac resolver file:\n%s", mac)
	}
	if got := SystemdResolvedDropIn("test"); !strings.Contains(got, "DNS=127.0.0.1:5354") || !strings.Contains(got, "Domains=~test") {
		t.Fatalf("systemd drop-in:\n%s", got)
	}
	if got := NetworkManagerDnsmasq("test"); got != "server=/test/127.0.0.1#5354\n" {
		t.Fatalf("nm conf:\n%s", got)
	}
	kind, err := SelectLinuxDNS(true, true)
	if err != nil || kind != ResolverSystemd {
		t.Fatalf("both backends: %s %v", kind, err)
	}
	kind, err = SelectLinuxDNS(false, true)
	if err != nil || kind != ResolverNM {
		t.Fatalf("nm only: %s %v", kind, err)
	}
	if _, err := SelectLinuxDNS(false, false); !errors.Is(err, ErrNoDNSIntegration) {
		t.Fatalf("neither: %v", err)
	}
	args := LinuxSetcapArgs("/usr/bin/bellhopd")
	if strings.Join(args, " ") != "cap_net_bind_service=+ep /usr/bin/bellhopd" {
		t.Fatalf("setcap args %v", args)
	}
	if strings.Join(LinuxClearcapArgs("/usr/bin/bellhopd"), " ") != "-r /usr/bin/bellhopd" {
		t.Fatalf("clear args %v", LinuxClearcapArgs("/usr/bin/bellhopd"))
	}
}
