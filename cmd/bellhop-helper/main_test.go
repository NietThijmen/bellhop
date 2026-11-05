package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/pkg/version"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: version.ID + "-helper " + version.Version},
		{name: "unknown op", args: []string{"dns", "install"}, wantCode: 1, wantStderr: `unknown operation "dns"`},
		{name: "unknown flag", args: []string{"--bogus"}, wantCode: 1, wantStderr: "unknown flag"},
		{name: "no args shows help", args: nil, wantCode: 0, wantStdout: "Privileged helper"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := run(tt.args, &out, &errb); code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errb.String())
			}
			if !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.wantStdout)
			}
			if !strings.Contains(errb.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", errb.String(), tt.wantStderr)
			}
		})
	}
}
