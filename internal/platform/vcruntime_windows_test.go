//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestVCRuntimeVersionThresholds(t *testing.T) {
	cases := []struct {
		major, minor int
		need         VCRedist
		ok           bool
	}{
		{14, 19, VCRedist2019, false},
		{14, 20, VCRedist2019, true},
		{14, 29, VCRedist2019, true},
		{14, 29, VCRedist2022, false},
		{14, 30, VCRedist2022, true},
		{14, 44, VCRedist2022, true},
		{15, 0, VCRedist2022, true},
		{13, 99, VCRedist2019, false},
		{14, 40, VCRedist(9), false},
	}
	for _, tc := range cases {
		if got := versionOK(tc.major, tc.minor, tc.need); got != tc.ok {
			t.Errorf("versionOK(%d.%d, %d) = %v, want %v", tc.major, tc.minor, tc.need, got, tc.ok)
		}
	}
}

func TestVCRuntimeMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "vcruntime140.dll")
	for _, need := range []VCRedist{VCRedist2019, VCRedist2022} {
		err := CheckVCRuntime(missing, need)
		if !errors.Is(err, ErrVCRedistMissing) {
			t.Fatalf("need %d: %v", need, err)
		}
		text := err.Error()
		if !strings.Contains(text, "vcruntime140.dll") {
			t.Fatalf("error does not name the DLL: %v", err)
		}
		var want string
		switch need {
		case VCRedist2019:
			want = "https://aka.ms/vs/16/release/vc_redist.x64.exe"
		case VCRedist2022:
			want = "https://aka.ms/vs/17/release/vc_redist.x64.exe"
		default:
			t.Fatalf("unhandled redist %d", need)
		}
		if !strings.Contains(text, want) {
			t.Fatalf("error does not point at the redistributable: %v", err)
		}
	}
	empty := filepath.Join(t.TempDir(), "empty.dll")
	if err := os.WriteFile(empty, []byte("not a dll"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckVCRuntime(empty, VCRedist2019); !errors.Is(err, ErrVCRedistMissing) {
		t.Fatalf("empty file: %v", err)
	}
}

func TestSystemVCRuntimeParses(t *testing.T) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, vcRuntimeDLL)
	if _, err := os.Stat(path); err != nil {
		t.Skip("System32 vcruntime140.dll is not installed")
	}
	major, minor, err := fileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	if major < 14 {
		t.Fatalf("parsed %d.%d from %s", major, minor, path)
	}
	if !versionOK(major, minor, VCRedist2019) && !versionOK(major, minor, VCRedist2022) {
		t.Fatalf("system runtime %d.%d satisfies neither redist", major, minor)
	}
}
