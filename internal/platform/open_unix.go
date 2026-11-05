//go:build unix

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// openerWait is how long the opener may take to report a failure. Some
// xdg-open fallbacks run the browser in the foreground and never return;
// those are left running once this passes.
const openerWait = 5 * time.Second

func openURL(u string) error     { return runOpener(u) }
func openPath(dir string) error { return runOpener(dir) }

// runOpener runs the platform opener (openTool) with target as its only
// argument. target is a validated http(s) URL or an absolute path, so it
// never starts with "-" and cannot be read as an option.
func runOpener(target string) error {
	path, err := exec.LookPath(openTool)
	if err != nil {
		return fmt.Errorf("%s not found: %w (%s)", openTool, err, openToolHint)
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), path, target)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", openTool, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return fmt.Errorf("%s failed: %w: %s", openTool, err, msg)
			}
			return fmt.Errorf("%s failed: %w", openTool, err)
		}
		return nil
	case <-time.After(openerWait):
		return nil
	}
}
