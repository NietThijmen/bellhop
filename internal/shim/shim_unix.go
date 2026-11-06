//go:build unix

package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func shimPath(dir, name string) string {
	return filepath.Join(dir, name)
}

func writeOne(dir, name, body string) error {
	return os.WriteFile(shimPath(dir, name), []byte(body), 0o755) //nolint:gosec // shims must be executable
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func phpBody(bellhop, line string) string {
	if line == "" {
		return "#!/bin/sh\nexec " + quote(bellhop) + " php -- \"$@\"\n"
	}
	return "#!/bin/sh\nexec " + quote(bellhop) + " php --use " + line + " -- \"$@\"\n"
}

func composerBody(bellhop string) string {
	return "#!/bin/sh\nexec " + quote(bellhop) + " composer -- \"$@\"\n"
}

// ScriptName is the file name of a shim on this OS.
func ScriptName(name string) string { return name }

// Launcher is the exec line a versioned or plain php shim contains.
func Launcher(bellhop, line string) string {
	if line == "" {
		return fmt.Sprintf("exec %s php -- \"$@\"", quote(bellhop))
	}
	return fmt.Sprintf("exec %s php --use %s -- \"$@\"", quote(bellhop), line)
}
