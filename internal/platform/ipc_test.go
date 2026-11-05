package platform

import (
	"bufio"
	"context"
	"errors"
	"testing"
	"time"
)

func TestIPCListenDialRoundTrip(t *testing.T) {
	addr := testIPCAddr(t)
	ln, err := ListenIPC(addr)
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		_, _ = c.Write([]byte("echo " + line))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := DialIPC(ctx, addr)
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("hi\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "echo hi\n" {
		t.Fatalf("got %q, want %q", got, "echo hi\n")
	}
}

func TestIPCListenInUse(t *testing.T) {
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

	ln2, err := ListenIPC(addr)
	if err == nil {
		ln2.Close()
		t.Fatal("second ListenIPC succeeded, want ErrIPCInUse")
	}
	if !errors.Is(err, ErrIPCInUse) {
		t.Fatalf("second ListenIPC error = %v, want ErrIPCInUse", err)
	}
}

func TestIPCDialNothingListening(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := DialIPC(ctx, testIPCAddr(t))
	if err == nil {
		c.Close()
		t.Fatal("DialIPC succeeded with nothing listening")
	}
}
