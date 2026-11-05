package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/pkg/version"
)

func TestRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.HomeEnv, home)
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: version.ID + "-helper " + version.Version},
		{name: "unknown op", args: []string{"nope"}, wantCode: 1, wantStderr: "unknown command"},
		{name: "unknown flag", args: []string{"--bogus"}, wantCode: 1, wantStderr: "unknown flag"},
		{name: "no args shows help", args: nil, wantCode: 0, wantStdout: "Privileged helper"},
		{name: "dns requires tld", args: []string{"dns"}, wantCode: 1, wantStderr: "tld"},
		{name: "setcap", args: []string{"setcap", "--binary", home + `\bellhopd.exe`}, wantCode: 0, wantStdout: "setcap is only used on Linux"},
	}
	if platform.BindCapSupported() {
		tests[len(tests)-1].wantStdout = ""
		tests[len(tests)-1].wantCode = 1
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := run(tt.args, &out, &errb); code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr %q stdout %q)", code, tt.wantCode, errb.String(), out.String())
			}
			if tt.wantStdout != "" && !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(errb.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", errb.String(), tt.wantStderr)
			}
		})
	}
}
