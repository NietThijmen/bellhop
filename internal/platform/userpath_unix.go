//go:build !windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureUserPath appends a snippet to the user shell profile when dir is absent.
func EnsureUserPath(dir string) error {
	return editProfile(func(text string) string {
		line := "export PATH=\"" + dir + ":$PATH\""
		if strings.Contains(text, dir) {
			return text
		}
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return text + "# bellhop\n" + line + "\n"
	})
}

// RemoveUserPath drops lines that mention dir from the profile snippet.
func RemoveUserPath(dir string) error {
	return editProfile(func(text string) string {
		var keep []string
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, dir) || line == "# bellhop" {
				continue
			}
			keep = append(keep, line)
		}
		return strings.Join(keep, "\n")
	})
}

func editProfile(edit func(string) string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".profile")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := edit(string(raw))
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil { //nolint:gosec // shell profiles are conventionally 0644
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
