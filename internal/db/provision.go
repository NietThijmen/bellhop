package db

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// identRe is the only shape of database and user name we will quote.
var identRe = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

// wpDefine matches define( then a quoted key. The value is parsed separately
// so only a matching literal quote is accepted.
var wpDefine = regexp.MustCompile(`(?m)define\(\s*(['"])(DB_[A-Z0-9_]+)`)

// Creds is a database login read from a project.
type Creds struct {
	Connection string
	Host       string
	Port       string
	Database   string
	Username   string
	Password   string
	Source     string
}

// Found reports a project whose database Bellhop will not change.
type Found struct {
	Creds  Creds
	Reason string
}

// Execer runs one statement. Placeholders are driver placeholders.
type Execer interface {
	Exec(ctx context.Context, query string, args ...any) error
}

// LaravelCreds reads a dotenv map. ok is false when DB_CONNECTION is not
// mysql or mariadb.
func LaravelCreds(env map[string]string) (Creds, bool) {
	c := Creds{
		Connection: env["DB_CONNECTION"],
		Host:       env["DB_HOST"],
		Port:       env["DB_PORT"],
		Database:   env["DB_DATABASE"],
		Username:   env["DB_USERNAME"],
		Password:   env["DB_PASSWORD"],
		Source:     ".env",
	}
	switch strings.ToLower(c.Connection) {
	case "mysql", "mariadb":
		return c, true
	default:
		return c, false
	}
}

// WordPressCreds reads literal define() lines. Non-literals are ignored.
// The caller must not write the file back.
func WordPressCreds(src string) Creds {
	c := Creds{Source: "wp-config.php", Connection: "mysql"}
	for _, loc := range wpDefine.FindAllStringSubmatchIndex(src, -1) {
		keyQuote := src[loc[2]:loc[3]]
		key := src[loc[4]:loc[5]]
		rest := src[loc[1]:]
		if rest == "" || string(rest[0]) != keyQuote {
			continue
		}
		val, ok := literalValue(rest[1:], keyQuote)
		if !ok {
			continue
		}
		switch key {
		case "DB_NAME":
			c.Database = val
		case "DB_USER":
			c.Username = val
		case "DB_PASSWORD":
			c.Password = val
		case "DB_HOST":
			c.Host, c.Port = splitHost(val)
		}
	}
	return c
}

// literalValue reads , 'value') when the quote matches the key's quote style.
func literalValue(rest, quote string) (string, bool) {
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, ",") {
		return "", false
	}
	rest = strings.TrimLeft(rest[1:], " \t")
	if rest == "" || string(rest[0]) != quote {
		return "", false
	}
	q := rest[0]
	var b strings.Builder
	for i := 1; i < len(rest); i++ {
		if rest[i] == q {
			return b.String(), true
		}
		if rest[i] == '\\' || rest[i] == '$' {
			return "", false
		}
		b.WriteByte(rest[i])
	}
	return "", false
}

func splitHost(v string) (string, string) {
	host, port, ok := strings.Cut(v, ":")
	if !ok {
		return v, ""
	}
	return host, port
}

// Local reports whether the server is this machine on our port.
// An empty port means 3306.
func Local(c Creds, ourPort int) bool {
	switch strings.ToLower(c.Host) {
	case "127.0.0.1", "localhost":
	default:
		return false
	}
	port := c.Port
	if port == "" {
		port = "3306"
	}
	n, err := strconv.Atoi(port)
	return err == nil && n == ourPort
}

// CheckIdent rejects names that cannot be quoted safely.
func CheckIdent(name string) error {
	if !identRe.MatchString(name) {
		return fmt.Errorf("%q is not a safe database name: use 1-64 letters, digits, underscore, '$' or '-'", name)
	}
	return nil
}

func quoteIdent(name string) string { return "`" + name + "`" }

// Provision creates the database and, when the user is not root, the account.
// The statements are idempotent.
func Provision(ctx context.Context, ex Execer, c Creds) error {
	if err := CheckIdent(c.Database); err != nil {
		return err
	}
	user := c.Username
	if user == "" {
		user = "root"
	}
	if user != "root" {
		if err := CheckIdent(user); err != nil {
			return err
		}
	}
	dbName := quoteIdent(c.Database)
	if err := ex.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+dbName+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		return err
	}
	if user == "root" {
		return nil
	}
	u := quoteIdent(user)
	for _, host := range []string{"localhost", "127.0.0.1"} {
		q := "CREATE USER IF NOT EXISTS " + u + "@'" + host + "' IDENTIFIED BY ?"
		if err := ex.Exec(ctx, q, c.Password); err != nil {
			return err
		}
		grant := "GRANT ALL ON " + dbName + ".* TO " + u + "@'" + host + "'"
		if err := ex.Exec(ctx, grant); err != nil {
			return err
		}
	}
	return nil
}

// MissingLaravel reports whether the dotenv has no DB_* keys.
func MissingLaravel(env map[string]string) bool {
	for _, k := range []string{"DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD"} {
		if env[k] != "" {
			return false
		}
	}
	return true
}

// DefaultLaravel is the dotenv Bellhop can write when the project has none.
func DefaultLaravel(database, user, password string, port int) map[string]string {
	if user == "" {
		user = "root"
	}
	return map[string]string{
		"DB_CONNECTION": "mysql",
		"DB_HOST":       "127.0.0.1",
		"DB_PORT":       strconv.Itoa(port),
		"DB_DATABASE":   database,
		"DB_USERNAME":   user,
		"DB_PASSWORD":   password,
	}
}
