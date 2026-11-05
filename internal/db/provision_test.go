package db

import (
	"context"
	"strings"
	"testing"
)

type fakeSQL struct {
	stmts []string
	args  [][]any
}

func (f *fakeSQL) Exec(_ context.Context, query string, args ...any) error {
	f.stmts = append(f.stmts, query)
	f.args = append(f.args, args)
	return nil
}

func TestRejectUnsafeIdents(t *testing.T) {
	for _, name := range []string{"bad`name", "a'b", "a;b", `a"b`, "", strings.Repeat("a", 65)} {
		if err := CheckIdent(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if err := CheckIdent("shop_db$1"); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionUserAndSecondRun(t *testing.T) {
	c := Creds{Database: "shop", Username: "shop", Password: "s3cret"}
	first := &fakeSQL{}
	if err := Provision(context.Background(), first, c); err != nil {
		t.Fatal(err)
	}
	second := &fakeSQL{}
	if err := Provision(context.Background(), second, c); err != nil {
		t.Fatal(err)
	}
	if strings.Join(first.stmts, "\n") != strings.Join(second.stmts, "\n") {
		t.Fatalf("second run differs\n%s\n%s", first.stmts, second.stmts)
	}
	joined := strings.Join(first.stmts, "\n")
	if !strings.Contains(joined, "CREATE DATABASE IF NOT EXISTS `shop`") || !strings.Contains(joined, "utf8mb4_unicode_ci") {
		t.Fatalf("database stmt = %s", joined)
	}
	if !strings.Contains(joined, "CREATE USER IF NOT EXISTS `shop`@'localhost' IDENTIFIED BY ?") {
		t.Fatalf("user stmt = %s", joined)
	}
	if !strings.Contains(joined, "GRANT ALL ON `shop`.* TO `shop`@'127.0.0.1'") {
		t.Fatalf("grant = %s", joined)
	}
	if first.args[1][0] != "s3cret" {
		t.Fatalf("password arg = %#v", first.args[1])
	}
}

func TestProvisionRootSkipsUser(t *testing.T) {
	f := &fakeSQL{}
	if err := Provision(context.Background(), f, Creds{Database: "shop", Username: "root"}); err != nil {
		t.Fatal(err)
	}
	if len(f.stmts) != 1 {
		t.Fatalf("stmts = %v", f.stmts)
	}
}

func TestWordPressLiteralsOnly(t *testing.T) {
	src := "<?php\ndefine('DB_NAME', 'wp');\ndefine('DB_USER', 'wp');\ndefine('DB_PASSWORD', 'pw');\ndefine('DB_HOST', '127.0.0.1:3307');\ndefine('DB_NAME', DB_NAME);\n"
	c := WordPressCreds(src)
	if c.Database != "wp" || c.Username != "wp" || c.Password != "pw" || c.Host != "127.0.0.1" || c.Port != "3307" {
		t.Fatalf("creds = %+v", c)
	}
}

func TestLocalHost(t *testing.T) {
	if !Local(Creds{Host: "localhost", Port: ""}, 3306) {
		t.Fatal("empty port should mean 3306")
	}
	if Local(Creds{Host: "db.example", Port: "3306"}, 3306) {
		t.Fatal("remote host was treated as local")
	}
	if Local(Creds{Host: "127.0.0.1", Port: "3307"}, 3306) {
		t.Fatal("other port was treated as ours")
	}
}

func TestLaravelConnection(t *testing.T) {
	_, ok := LaravelCreds(map[string]string{"DB_CONNECTION": "sqlite"})
	if ok {
		t.Fatal("sqlite was accepted")
	}
	c, ok := LaravelCreds(map[string]string{"DB_CONNECTION": "mariadb", "DB_DATABASE": "app"})
	if !ok || c.Database != "app" {
		t.Fatalf("creds = %+v ok %v", c, ok)
	}
}
