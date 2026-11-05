package db

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// command is replaced in tests.
var command = exec.CommandContext

// MigrateArgs is php artisan migrate. It never includes --force or migrate:fresh.
func MigrateArgs() []string { return []string{"artisan", "migrate"} }

// Import streams r into the mariadb client. gzipped input is decompressed
// while it is copied, not loaded into memory.
func Import(ctx context.Context, client string, args []string, r io.Reader, gzipped bool) error {
	if gzipped {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	cmd := command(ctx, client, args...)
	cmd.Stdin = r
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mariadb import: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Export streams mariadb-dump into w as gzip. The dump is not buffered.
func Export(ctx context.Context, dump string, args []string, w io.Writer) error {
	cmd := command(ctx, dump, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	_, copyErr := io.Copy(gz, stdout)
	closeErr := gz.Close()
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if waitErr != nil {
		return fmt.Errorf("mariadb-dump: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// OpenSQL opens a .sql or .sql.gz file for streaming.
func OpenSQL(path string) (io.ReadCloser, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	return f, strings.HasSuffix(strings.ToLower(path), ".gz"), nil
}

// ClientArgs is the mariadb client invocation for one database.
func ClientArgs(port int, database string) []string {
	return []string{"--user=root", "--host=127.0.0.1", "--port=" + fmt.Sprintf("%d", port), "--protocol=tcp", database}
}

// ConfirmDrop requires the typed name to match the database being removed.
func ConfirmDrop(name, confirm string) error {
	if err := CheckIdent(name); err != nil {
		return err
	}
	if confirm != name {
		return fmt.Errorf("refusing to drop %s: pass --confirm %s", name, name)
	}
	return nil
}
