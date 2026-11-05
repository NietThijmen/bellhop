package platform

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// OpenURL opens an http or https URL in the user's default browser. Any
// other scheme is refused, so a value from the daemon or a project can never
// be used to launch a program.
func OpenURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("cannot open %q in a browser: %w", rawURL, err)
	}
	if s := strings.ToLower(u.Scheme); (s != "http" && s != "https") || u.Host == "" {
		return fmt.Errorf("cannot open %q in a browser: only http:// and https:// URLs are supported", rawURL)
	}
	if err := openURL(u.String()); err != nil {
		return fmt.Errorf("open %s in the default browser: %w (open it manually)", u, err)
	}
	return nil
}

// OpenPath opens an existing directory in the platform's file manager.
func OpenPath(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot open folder %q: %w", dir, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("cannot open folder %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("cannot open %s: %w", abs, errNotDir)
	}
	if err := openPath(abs); err != nil {
		return fmt.Errorf("open folder %s in the file manager: %w (open it manually)", abs, err)
	}
	return nil
}

var errNotDir = errors.New("not a directory")
