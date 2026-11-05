//go:build windows

package platform

import (
	"strings"
	"testing"
)

func TestInstallResolverCommand(t *testing.T) {
	var script string
	orig := CommandOutput
	CommandOutput = func(name string, args ...string) ([]byte, error) {
		if name != "powershell.exe" {
			t.Errorf("command %s", name)
		}
		script = strings.Join(args, " ")
		return nil, nil
	}
	t.Cleanup(func() { CommandOutput = orig })

	change, err := InstallResolver("test")
	if err != nil {
		t.Fatal(err)
	}
	if change.Kind != ResolverNRPT || change.TLD != "test" {
		t.Fatalf("change = %+v", change)
	}
	if !strings.Contains(script, "Add-DnsClientNrptRule") || !strings.Contains(script, ".test") {
		t.Fatalf("script %s", script)
	}
	if _, err := InstallResolver("not a tld"); err == nil {
		t.Fatal("invalid tld ran a command")
	}
}

func TestElevateScript(t *testing.T) {
	script := ElevateScript(`C:\Program Files\bellhop-helper.exe`, []string{"apply", "--tld", "test"})
	if !strings.Contains(script, "-Verb RunAs -Wait") || !strings.Contains(script, "'apply','--tld','test'") {
		t.Fatalf("script %s", script)
	}
}

func TestPortOwnerScript(t *testing.T) {
	script := PortOwnerScript(80)
	if !strings.Contains(script, "Get-NetTCPConnection -LocalPort 80") || !strings.Contains(script, "OwningProcess") {
		t.Fatalf("script:\n%s", script)
	}
}
