package drivers

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// bedrockDriver: Roots Bedrock, WordPress core in web/wp, docroot web.
type bedrockDriver struct{}

func (bedrockDriver) Name() string { return "bedrock" }

func (bedrockDriver) Detect(dir string) bool {
	return isFile(filepath.Join(dir, "web", "wp", "wp-load.php"))
}

func (d bedrockDriver) Plan(dir string) ServePlan {
	plan := phpPlan(d.Name(), dir, filepath.Join(dir, "web"))
	if isSubdirMultisite(filepath.Join(dir, "config", "application.php")) {
		plan.Rewrites = bedrockMultisiteRewrites()
	}
	return plan
}

// wordpressDriver: wp-load.php or wp-config.php in the project root.
type wordpressDriver struct{}

func (wordpressDriver) Name() string { return "wordpress" }

func (wordpressDriver) Detect(dir string) bool {
	return isFile(filepath.Join(dir, "wp-load.php")) || isFile(filepath.Join(dir, "wp-config.php"))
}

func (d wordpressDriver) Plan(dir string) ServePlan {
	plan := phpPlan(d.Name(), dir, dir)
	if isSubdirMultisite(filepath.Join(dir, "wp-config.php")) {
		plan.Rewrites = wordpressMultisiteRewrites()
	}
	return plan
}

// siteSegment matches the leading "/<subsite>" of a subdirectory multisite
// path, as in WordPress's documented nginx rules.
const siteSegment = `^/[_0-9a-zA-Z-]+`

// wordpressMultisiteRewrites are the standard subdirectory-multisite rules:
//
//	rewrite ^/[_0-9a-zA-Z-]+(/wp-(admin|content|includes).*) $1 last;
//	rewrite ^/[_0-9a-zA-Z-]+(/.*\.php)$ $1 last;
//
// guarded by "if (!-e $request_filename)" so real files are never rewritten.
func wordpressMultisiteRewrites() []Rewrite {
	return []Rewrite{
		{Match: siteSegment + `(/wp-(admin|content|includes).*)$`, Replace: "$1", IfNotExists: true},
		{Match: siteSegment + `(/.*\.php)$`, Replace: "$1", IfNotExists: true},
	}
}

// bedrockMultisiteRewrites are the same rules for Bedrock's layout, where
// core lives in /wp and content in /app (Bedrock's default CONTENT_DIR).
// They cover both /site2/wp/wp-admin/ (multisite-url-fixer style URLs) and
// /site2/wp-admin/.
func bedrockMultisiteRewrites() []Rewrite {
	return []Rewrite{
		{Match: siteSegment + `(/wp/.*)$`, Replace: "$1", IfNotExists: true},
		{Match: siteSegment + `(/app/.*)$`, Replace: "$1", IfNotExists: true},
		{Match: siteSegment + `(/wp-(admin|includes).*)$`, Replace: "/wp$1", IfNotExists: true},
		{Match: siteSegment + `(/.*\.php)$`, Replace: "/wp$1", IfNotExists: true},
	}
}

var (
	reMultisite = regexp.MustCompile(`(?i)define\s*\(\s*['"]MULTISITE['"]\s*,\s*(true|1)\s*\)`)
	reSubdomain = regexp.MustCompile(`(?i)define\s*\(\s*['"]SUBDOMAIN_INSTALL['"]\s*,\s*(true|1)\s*\)`)
)

// maxConfigBytes caps how much of a config file is scanned.
const maxConfigBytes = 1 << 20

// isSubdirMultisite reports whether the PHP config file at path literally
// defines MULTISITE as true and does not define SUBDOMAIN_INSTALL as true.
// "define(" also matches Bedrock's Config::define(). Lines that start as a
// comment (//, #, /*, *) are ignored. Values computed at runtime (env(...))
// are not evaluated: such installs get no rewrites, which is the
// conservative choice.
func isSubdirMultisite(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	multisite, subdomain := false, false
	sc := bufio.NewScanner(io.LimitReader(f, maxConfigBytes))
	sc.Buffer(make([]byte, 0, 64*1024), maxConfigBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "*") {
			continue
		}
		if reMultisite.MatchString(line) {
			multisite = true
		}
		if reSubdomain.MatchString(line) {
			subdomain = true
		}
	}
	return multisite && !subdomain
}
