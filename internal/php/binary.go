package php

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func findBinary(dir string, names []string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no PHP binary names configured for %s", dir)
	}
	for _, name := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		return abs, nil
	}
	return "", fmt.Errorf("cannot find a PHP binary in %s (looked for %s); remove the install and install it again", dir, strings.Join(names, ", "))
}
