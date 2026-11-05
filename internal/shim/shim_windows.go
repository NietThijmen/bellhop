//go:build !unix

package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func shimPath(dir, name string) string {
	return filepath.Join(dir, name+".cmd")
}

func writeOne(dir, name, body string) error {
	return os.WriteFile(shimPath(dir, name), []byte(body), 0o644)
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func phpBody(bellhop, line string) string {
	if line == "" {
		return "@echo off\r\n" + quote(bellhop) + " php -- %*\r\n"
	}
	return "@echo off\r\n" + quote(bellhop) + " php --use " + line + " -- %*\r\n"
}

func composerBody(bellhop string) string {
	return "@echo off\r\n" + quote(bellhop) + " composer -- %*\r\n"
}

// ScriptName is the file name of a shim on this OS.
func ScriptName(name string) string {
	return name + ".cmd"
}

// QuoteForTest exposes the launcher line used in scripts.
func Launcher(bellhop, line string) string {
	if line == "" {
		return fmt.Sprintf("%s php -- %%*", quote(bellhop))
	}
	return fmt.Sprintf("%s php --use %s -- %%*", quote(bellhop), line)
}
