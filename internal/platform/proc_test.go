package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const procHelperEnv = "BELLHOP_PROC_HELPER"

// TestHelperProcess is not a real test: it is re-executed by the tests below
// as a child process (the os/exec helper-process pattern).
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(procHelperEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	switch mode {
	case "sleep":
		time.Sleep(2 * time.Minute)
	case "spawn":
		gc := helperCmd("sleep")
		if err := gc.Start(); err != nil {
			os.Exit(3)
		}
		pidFile := os.Getenv("BELLHOP_PROC_PIDFILE")
		tmp := pidFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(strconv.Itoa(gc.Process.Pid)), 0o600); err != nil {
			os.Exit(4)
		}
		if err := os.Rename(tmp, pidFile); err != nil {
			os.Exit(5)
		}
		time.Sleep(2 * time.Minute)
	}
	os.Exit(0)
}

func helperCmd(mode string, extraEnv ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(append(os.Environ(), procHelperEnv+"="+mode), extraEnv...)
	return cmd
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProcessGroupLeavesNoOrphans(t *testing.T) {
	type stopFn func(t *testing.T, g ProcessGroup)
	cases := []struct {
		name    string
		windows bool // only meaningful on Windows
		stop    stopFn
	}{
		{"kill", false, func(t *testing.T, g ProcessGroup) {
			if err := g.Kill(); err != nil {
				t.Fatalf("Kill: %v", err)
			}
		}},
		{"terminate", false, func(t *testing.T, g ProcessGroup) {
			err := g.Terminate()
			if errors.Is(err, ErrGracefulStopUnsupported) {
				t.Skip("graceful stop unsupported on this platform")
			}
			if err != nil {
				t.Fatalf("Terminate: %v", err)
			}
		}},
		{"close-job", true, func(t *testing.T, g ProcessGroup) {
			// Closing the Job Object alone must kill everything in it
			// (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); this is what happens when
			// the daemon crashes.
			if err := g.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.windows && runtime.GOOS != "windows" {
				t.Skip("Job Objects are Windows only")
			}
			pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
			cmd := helperCmd("spawn", "BELLHOP_PROC_PIDFILE="+pidFile)
			g, err := StartProcessGroup(cmd)
			if err != nil {
				t.Fatalf("StartProcessGroup: %v", err)
			}
			waited := make(chan struct{})
			go func() { _ = cmd.Wait(); close(waited) }()
			t.Cleanup(func() {
				_ = g.Kill()
				_ = g.Close()
				<-waited
			})

			var gcPID int
			waitFor(t, "grandchild pid file", 10*time.Second, func() bool {
				b, err := os.ReadFile(pidFile)
				if err != nil {
					return false
				}
				gcPID, err = strconv.Atoi(strings.TrimSpace(string(b)))
				return err == nil
			})
			if !ProcessAlive(gcPID) {
				t.Fatalf("grandchild %d not alive after start", gcPID)
			}
			if !ProcessAlive(cmd.Process.Pid) {
				t.Fatalf("child %d not alive after start", cmd.Process.Pid)
			}

			tc.stop(t, g)

			select {
			case <-waited:
			case <-time.After(10 * time.Second):
				t.Fatal("child did not exit")
			}
			waitFor(t, "grandchild to exit", 10*time.Second, func() bool { return !ProcessAlive(gcPID) })
		})
	}
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("own process reported dead")
	}
	if ProcessAlive(0) || ProcessAlive(-1) {
		t.Fatal("non-positive pid reported alive")
	}
	cmd := helperCmd("exit")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run helper: %v", err)
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Fatalf("exited process %d reported alive", cmd.Process.Pid)
	}
}

func TestKillAfterExitIsNoError(t *testing.T) {
	cmd := helperCmd("exit")
	g, err := StartProcessGroup(cmd)
	if err != nil {
		t.Fatalf("StartProcessGroup: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := g.Kill(); err != nil {
		t.Fatalf("Kill after exit: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
