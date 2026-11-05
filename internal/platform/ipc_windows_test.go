package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// testIPCAddr returns a unique named pipe address.
func testIPCAddr(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return pipePrefix + "bellhop-test-" + hex.EncodeToString(b)
}

func TestIPCDefaultAddress(t *testing.T) {
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	got := DefaultIPCAddress(Dirs{})
	if want := pipePrefix + "bellhopd-" + sid.String(); got != want {
		t.Fatalf("DefaultIPCAddress = %q, want %q", got, want)
	}
}

// Pipe names share one machine-wide namespace, so different accounts must
// never map to the same address (CORP\bob vs PC\bob, José vs Josè, ...).
func TestIPCPipeAddressUniquePerAccount(t *testing.T) {
	sids := []string{
		"S-1-5-21-1111111111-2222222222-3333333333-1001", // e.g. CORP\bob
		"S-1-5-21-4444444444-5555555555-6666666666-1001", // e.g. PC\bob
		"S-1-5-21-4444444444-5555555555-6666666666-1002",
		"S-1-5-18",
	}
	seen := map[string]string{}
	for _, s := range sids {
		addr := pipeAddressForSID(s)
		if prev, dup := seen[addr]; dup {
			t.Fatalf("SIDs %s and %s share pipe address %s", prev, s, addr)
		}
		seen[addr] = s
		name := strings.TrimPrefix(addr, pipePrefix)
		if strings.ContainsAny(name, `\/ `) {
			t.Errorf("pipe address %q contains a separator", addr)
		}
		for _, r := range name {
			if r > 0x7f {
				t.Errorf("pipe address %q is not ASCII", addr)
				break
			}
		}
	}
}

func foreignSID(t *testing.T) *windows.SID {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	me, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if sid.Equals(me) {
		t.Skip("test runs as LocalSystem; cannot build a foreign SID")
	}
	return sid
}

// A pipe served by a process running as someone else must be rejected by
// clients. We are that "someone else" here: dialing while expecting a
// foreign owner simulates a squatted pipe.
func TestIPCDialRejectsForeignOwner(t *testing.T) {
	addr := testIPCAddr(t)
	ln, err := ListenIPC(addr)
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialIPC(ctx, addr, foreignSID(t))
	if err == nil {
		c.Close()
		t.Fatal("dialIPC accepted a pipe served by another account")
	}
	if !errors.Is(err, errForeignPipeOwner) {
		t.Fatalf("dialIPC error = %v, want errForeignPipeOwner", err)
	}

	// The real owner still connects.
	c, err = DialIPC(ctx, addr)
	if err != nil {
		t.Fatalf("DialIPC as owner: %v", err)
	}
	c.Close()
}

// A daemon that finds its pipe name taken by another account must say so,
// not claim it is "already running".
func TestIPCListenSquattedPipe(t *testing.T) {
	addr := testIPCAddr(t)
	// The squatter: a permissive pipe created first.
	squat, err := listenIPC(addr, mustSID(t, "S-1-1-0")) // Everyone
	if err != nil {
		t.Fatalf("create squatting pipe: %v", err)
	}
	defer squat.Close()
	go func() {
		for {
			c, err := squat.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	ln, err := listenIPC(addr, foreignSID(t))
	if err == nil {
		ln.Close()
		t.Fatal("listenIPC succeeded on a squatted pipe")
	}
	if errors.Is(err, ErrIPCInUse) {
		t.Fatalf("listenIPC reported ErrIPCInUse for a pipe owned by another account: %v", err)
	}
	if !errors.Is(err, errForeignPipeOwner) {
		t.Fatalf("listenIPC error = %v, want errForeignPipeOwner", err)
	}
}

func mustSID(t *testing.T, s string) *windows.SID {
	t.Helper()
	sid, err := windows.StringToSid(s)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func TestIPCSecurityDescriptorCurrentUserOnly(t *testing.T) {
	sddl, err := currentUserSDDL()
	if err != nil {
		t.Fatal(err)
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	want := "D:P(A;;GA;;;" + tu.User.Sid.String() + ")"
	if sddl != want {
		t.Fatalf("sddl = %q, want %q", sddl, want)
	}
	if _, err := windows.SecurityDescriptorFromString(sddl); err != nil {
		t.Fatalf("sddl does not parse: %v", err)
	}
}

func TestIPCListenRejectsNonPipeAddress(t *testing.T) {
	ln, err := ListenIPC(`C:\not\a\pipe`)
	if err == nil {
		ln.Close()
		t.Fatal("ListenIPC accepted a non-pipe address")
	}
}
