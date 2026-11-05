//go:build !unix

package php

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
