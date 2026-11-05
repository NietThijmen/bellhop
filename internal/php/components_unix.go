//go:build unix

package php

func artifactComponents() []string {
	return []string{ComponentCLI, ComponentFPM}
}

func primaryComponent() string { return ComponentFPM }

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
