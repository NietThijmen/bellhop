//go:build !unix

package php

// unixPHP is false on Windows: the catalog row is one php zip, and the
// pool is php-cgi rather than php-fpm.
const unixPHP = false

func artifactComponents() []string { return []string{ComponentWindows} }

func primaryComponent() string { return ComponentWindows }

func binaryNames(component string) []string {
	switch component {
	case ComponentWindows:
		return []string{"php.exe", "php"}
	default:
		return nil
	}
}

// cgiBinaryNames is the Windows FastCGI executable, resolved explicitly so
// a php-cgi pool never starts the CLI php.exe.
func cgiBinaryNames() []string { return []string{"php-cgi.exe"} }
