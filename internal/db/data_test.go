package db

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("BELLHOP_SQL_HELPER") == "1" {
		os.Exit(sqlHelper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func sqlHelper(args []string) int {
	if len(args) != 2 {
		return 2
	}
	switch args[0] {
	case "import":
		f, err := os.Create(args[1])
		if err != nil {
			return 1
		}
		_, err = io.Copy(f, os.Stdin)
		_ = f.Close()
		if err != nil {
			return 1
		}
	case "dump":
		f, err := os.Open(args[1])
		if err != nil {
			return 1
		}
		_, err = io.Copy(os.Stdout, f)
		_ = f.Close()
		if err != nil {
			return 1
		}
	default:
		return 2
	}
	return 0
}

func TestConfirmDrop(t *testing.T) {
	if err := ConfirmDrop("shop", "shop"); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmDrop("shop", "other"); err == nil {
		t.Fatal("mismatched confirm was accepted")
	}
	if err := ConfirmDrop("a;b", "a;b"); err == nil {
		t.Fatal("unsafe name was accepted")
	}
}

func TestMigrateArgsNeverForce(t *testing.T) {
	args := MigrateArgs()
	if len(args) != 2 || args[0] != "artisan" || args[1] != "migrate" {
		t.Fatalf("args = %q", args)
	}
	for _, a := range args {
		if a == "--force" || a == "migrate:fresh" {
			t.Fatalf("forbidden arg %s", a)
		}
	}
}

func TestImportExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.sql.gz")
	dst := filepath.Join(dir, "out.sql.gz")
	stored := filepath.Join(dir, "stored.sql")
	if err := writeIncompressible(src, 100<<20); err != nil {
		t.Fatal(err)
	}
	prev := command
	command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		mode := "import"
		if filepath.Base(name) == "dump" {
			mode = "dump"
		}
		cmd := exec.CommandContext(ctx, os.Args[0], mode, stored)
		cmd.Env = append(os.Environ(), "BELLHOP_SQL_HELPER=1")
		return cmd
	}
	t.Cleanup(func() { command = prev })
	in, gzipped, err := OpenSQL(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := Import(context.Background(), "client", nil, in, gzipped); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), "dump", nil, out); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if a, b := hashGunzip(t, src), hashGunzip(t, dst); a != b {
		t.Fatalf("round trip hash %x vs %x", a, b)
	}
}

func writeIncompressible(path string, n int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	buf := bytes.Repeat([]byte("insert into t values (1);\n"), 1024)
	var written int64
	for written < n {
		chunk := buf
		if int64(len(chunk)) > n-written {
			chunk = chunk[:n-written]
		}
		if _, err := gz.Write(chunk); err != nil {
			return err
		}
		written += int64(len(chunk))
	}
	return gz.Close()
}

func hashGunzip(t *testing.T, path string) [32]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, gz); err != nil {
		t.Fatal(err)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}
