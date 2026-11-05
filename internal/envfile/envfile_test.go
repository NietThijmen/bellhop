package envfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPreservesCRLFQuotesAndOrder(t *testing.T) {
	in := []byte("# database\r\nDB_CONNECTION=mysql\r\nDB_HOST=127.0.0.1\r\nDB_PASSWORD=\"secret\"\r\n\r\n# end\r\n")
	got, changes := Apply(in, []string{"DB_HOST", "DB_DATABASE"}, map[string]string{
		"DB_HOST":     "localhost",
		"DB_DATABASE": "shop",
	})
	want := []byte("# database\r\nDB_CONNECTION=mysql\r\nDB_HOST=localhost\r\nDB_PASSWORD=\"secret\"\r\n\r\n# end\r\nDB_DATABASE=shop\r\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if len(changes) != 2 || changes[0].Old != "127.0.0.1" || changes[0].Added || !changes[1].Added {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestApplyKeepsQuotesAndComments(t *testing.T) {
	in := []byte("NAME='old' # keep\nPLAIN=one\n")
	got, _ := Apply(in, []string{"NAME", "PLAIN"}, map[string]string{"NAME": "new", "PLAIN": "two"})
	want := []byte("NAME='new' # keep\nPLAIN=two\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestUpdateWritesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	orig := []byte("DB_HOST=127.0.0.1\r\n")
	if err := os.WriteFile(path, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(path, []string{"DB_PORT"}, map[string]string{"DB_PORT": "3306"}); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(path + BackupSuffix)
	if err != nil || !bytes.Equal(bak, orig) {
		t.Fatalf("backup = %q, %v", bak, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("DB_HOST=127.0.0.1\r\nDB_PORT=3306\r\n")
	if !bytes.Equal(body, want) {
		t.Fatalf("body = %q", body)
	}
	vals, err := Read(path)
	if err != nil || vals["DB_HOST"] != "127.0.0.1" || vals["DB_PORT"] != "3306" {
		t.Fatalf("read = %v, %v", vals, err)
	}
}
