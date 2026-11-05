//go:build unix

package php

// unixPHP selects the php-cli and php-fpm archives. Windows uses one zip.
const unixPHP = true

func artifactComponents() []string {
	return []string{ComponentCLI, ComponentFPM}
}

func primaryComponent() string { return ComponentFPM }

func cgiBinaryNames() []string { return nil }

func binaryNames(component string) []string {
	switch component {
	case ComponentCLI:
		return []string{"bin/php", "php"}
	case ComponentFPM:
		return []string{"sbin/php-fpm", "bin/php-fpm", "php-fpm"}
	default:
		return nil
	}
}
