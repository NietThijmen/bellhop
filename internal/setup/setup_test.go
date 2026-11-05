package setup

import (
	"errors"
	"reflect"
	"testing"
)

func TestSetupRefusesForeignPort(t *testing.T) {
	err := Setup(Request{Helper: "helper"}, Hooks{
		PortOwner: func(port int) (string, error) {
			if port == 80 {
				return "nginx", nil
			}
			return "", nil
		},
		Elevate: func(string, []string) error { t.Fatal("elevated"); return nil },
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Port != 80 || conflict.Owner != "nginx" {
		t.Fatalf("err = %v", err)
	}
	if conflict.Hint() != "stop nginx, then `bellhop setup` again" {
		t.Fatalf("hint = %s", conflict.Hint())
	}
}

func TestSetupOneElevation(t *testing.T) {
	var got []string
	err := Setup(Request{
		TLD: "test", CertPath: `C:\ca.crt`, Binary: `C:\bellhopd.exe`,
		Helper: "helper", BinDir: `C:\bin`,
	}, Hooks{
		PortOwner: func(int) (string, error) { return "bellhopd.exe", nil },
		Elevate: func(helper string, args []string) error {
			got = append([]string{helper}, args...)
			return nil
		},
		InstallAutostart: func(binary string) error {
			if binary != `C:\bellhopd.exe` {
				t.Fatalf("autostart %s", binary)
			}
			return nil
		},
		AddPath: func(dir string) error { return nil },
		InstallPHP: func(version string) error {
			if version != "8.3" {
				t.Fatalf("php %s", version)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"helper", "apply", "--tld", "test", "--cert", `C:\ca.crt`, "--binary", `C:\bellhopd.exe`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("elevate = %v", got)
	}
}

func TestSetupSkipsDNSInLocalhostMode(t *testing.T) {
	args := ApplyArgs(Request{Localhost: true, TLD: "test", CertPath: "ca.crt"})
	if reflect.DeepEqual(args, []string{"apply", "--tld", "test", "--cert", "ca.crt"}) {
		t.Fatal("localhost mode still routes DNS")
	}
	if !reflect.DeepEqual(args, []string{"apply", "--cert", "ca.crt"}) {
		t.Fatalf("args = %v", args)
	}
}

func TestUninstallPurgeConfirm(t *testing.T) {
	hooks := Hooks{Databases: func() ([]string, error) { return []string{"shop"}, nil }}
	names, err := Uninstall(Request{Purge: true}, hooks)
	if err == nil || len(names) != 1 || names[0] != "shop" {
		t.Fatalf("names %v err %v", names, err)
	}
	var undone bool
	hooks.Elevate = func(string, []string) error { undone = true; return nil }
	if _, err := Uninstall(Request{Purge: true, Confirm: true, Helper: "helper"}, hooks); err != nil {
		t.Fatal(err)
	}
	if !undone {
		t.Fatal("confirmed purge did not undo")
	}
}

func TestDoctor(t *testing.T) {
	checks := Doctor(DoctorInput{HTTPPort: 80, HTTPSPort: 443, DNSRecorded: true, Trusted: true})
	for _, check := range checks {
		if !check.OK {
			t.Fatalf("%s: %s", check.Name, check.Detail)
		}
	}
	bad := Doctor(DoctorInput{DaemonErr: errors.New("down")})
	if bad[0].OK || bad[0].Name != "daemon" {
		t.Fatalf("daemon check = %+v", bad[0])
	}
}

func TestUninstallOnlyRecordedHelper(t *testing.T) {
	var args []string
	_, err := Uninstall(Request{Helper: "helper", BinDir: "bin"}, Hooks{
		Elevate:         func(_ string, a []string) error { args = a; return nil },
		RemoveAutostart: func() error { return nil },
		RemovePath:      func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"undo"}) {
		t.Fatalf("args = %v", args)
	}
}
