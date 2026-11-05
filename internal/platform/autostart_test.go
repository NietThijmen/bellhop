package platform

import (
	"strings"
	"testing"
)

func TestAutostartArgs(t *testing.T) {
	create := strings.Join(SchtasksCreateArgs("bellhop", `C:\bellhopd.exe`), " ")
	for _, want := range []string{"/Create", "/F", "/SC ONLOGON", "/TN bellhop", "/RL LIMITED", `C:\bellhopd.exe`} {
		if !strings.Contains(create, want) {
			t.Fatalf("create %q missing %q", create, want)
		}
	}
	if !strings.Contains(SystemdUserUnit("/usr/bin/bellhopd"), "ExecStart=/usr/bin/bellhopd") {
		t.Fatal(SystemdUserUnit("/usr/bin/bellhopd"))
	}
	if !strings.Contains(LaunchAgentPlist("io.bellhop", "/bin/bellhopd"), "<key>RunAtLoad</key><true/>") {
		t.Fatal("plist missing RunAtLoad")
	}
}
