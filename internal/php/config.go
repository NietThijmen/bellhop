package php

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func joinSocket(runDir, line string) string {
	return filepath.Join(runDir, "php-"+line+".sock")
}

func userIniPath(dir string) string {
	return filepath.Join(dir, "conf.d", FileUserIni)
}

func pidFile(runDir, line string) string {
	return filepath.Join(runDir, "php-"+line+".pid")
}

// writePHPConfig writes php.ini and conf.d/10-bellhop.ini, creating
// conf.d/99-user.ini when it is absent. An existing 99-user.ini is left
// byte-for-byte unchanged, including when it is empty.
func writePHPConfig(dir, timezone string) error {
	if dir == "" {
		return errors.New("php config: install directory is empty")
	}
	if err := writeFile(filepath.Join(dir, FilePHPIni), mainIni()); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "conf.d", FileBellhop), bellhopIni(cleanTimezone(timezone))); err != nil {
		return err
	}
	return ensureUserIni(dir)
}

func mainIni() []byte {
	return []byte(strings.Join([]string{
		"; Managed by Bellhop. This file is overwritten.",
		"; Files in conf.d are loaded through PHP_INI_SCAN_DIR.",
		"; Put your own settings in conf.d/99-user.ini.",
		"",
		"[PHP]",
		"engine = On",
		"",
	}, "\n"))
}

func bellhopIni(tz string) []byte {
	return []byte(strings.Join([]string{
		"; Managed by Bellhop. This file is overwritten.",
		"; Put your own settings in 99-user.ini.",
		"",
		"memory_limit = 512M",
		"upload_max_filesize = 512M",
		"post_max_size = 512M",
		"max_execution_time = 300",
		"display_errors = On",
		"error_reporting = E_ALL",
		"opcache.enable = 1",
		"opcache.validate_timestamps = 1",
		"date.timezone = " + tz,
		"",
	}, "\n"))
}

// writeFPMConfig writes the php-fpm master and pool file for one line.
// The pool listens on SocketPath(runDir, version), uses pm=ondemand and
// allows at most 10 children. user and group are the account the workers
// run as; empty values omit those directives.
func writeFPMConfig(dir, runDir, version, user, group string) (string, error) {
	line, err := Line(version)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", errors.New("php-fpm config: install directory is empty")
	}
	sock, err := SocketPath(runDir, line)
	if err != nil {
		return "", err
	}
	body := renderFPM(fpmConfig{
		pid:      pidFile(runDir, line),
		errorLog: filepath.Join(dir, "php-fpm.log"),
		listen:   sock,
		user:     user,
		group:    group,
	})
	path := filepath.Join(dir, FileFPMConf)
	if err := writeFile(path, body); err != nil {
		return "", err
	}
	return path, nil
}

type fpmConfig struct {
	pid      string
	errorLog string
	listen   string
	user     string
	group    string
}

func renderFPM(c fpmConfig) []byte {
	var b strings.Builder
	b.WriteString("; Managed by Bellhop. This file is overwritten when the pool is prepared.\n\n")
	b.WriteString("[global]\n")
	b.WriteString("daemonize = no\n")
	fmt.Fprintf(&b, "pid = %s\n", quoteFPM(c.pid))
	fmt.Fprintf(&b, "error_log = %s\n\n", quoteFPM(c.errorLog))
	b.WriteString("[www]\n")
	fmt.Fprintf(&b, "listen = %s\n", quoteFPM(c.listen))
	b.WriteString("listen.mode = 0660\n")
	b.WriteString("pm = ondemand\n")
	b.WriteString("pm.max_children = 10\n")
	b.WriteString("pm.process_idle_timeout = 10s\n")
	b.WriteString("clear_env = no\n")
	b.WriteString("catch_workers_output = yes\n")
	if c.user != "" {
		fmt.Fprintf(&b, "user = %s\n", quoteFPM(c.user))
	}
	if c.group != "" {
		fmt.Fprintf(&b, "group = %s\n", quoteFPM(c.group))
	}
	return []byte(b.String())
}

// quoteFPM wraps a pool value in double quotes without changing the bytes
// of the value itself, so a path remains a literal substring of the file.
func quoteFPM(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func ensureUserIni(dir string) error {
	path := userIniPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
