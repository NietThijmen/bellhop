//go:build windows

package platform

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// EnsureUserPath appends dir to the current user's PATH when it is absent.
func EnsureUserPath(dir string) error {
	return editUserPath(func(parts []string) []string {
		for _, p := range parts {
			if strings.EqualFold(p, dir) {
				return parts
			}
		}
		return append(parts, dir)
	})
}

// RemoveUserPath drops dir from the current user's PATH.
func RemoveUserPath(dir string) error {
	return editUserPath(func(parts []string) []string {
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if !strings.EqualFold(p, dir) {
				out = append(out, p)
			}
		}
		return out
	})
}

func editUserPath(edit func([]string) []string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open user environment: %w", err)
	}
	defer func() { _ = key.Close() }()
	current, _, err := key.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("read user PATH: %w", err)
	}
	parts := splitPath(current)
	next := edit(parts)
	if err := key.SetStringValue("Path", strings.Join(next, ";")); err != nil {
		return fmt.Errorf("write user PATH: %w", err)
	}
	return nil
}

func splitPath(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	raw := strings.Split(s, ";")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
