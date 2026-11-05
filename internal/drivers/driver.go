// Package drivers inspects a project directory and decides how to serve it.
// Everything in here is a pure function of the filesystem: no I/O besides
// stat/read of the project, no knowledge of Caddy or PHP pools.
package drivers

// Driver detects one kind of project and produces its serve plan.
type Driver interface {
	// Name is the stable identifier stored in the sites table ("laravel", ...).
	Name() string
	// Detect reports whether dir (an absolute project root) is this kind of project.
	Detect(dir string) bool
	// Plan describes how to serve dir. Only called after Detect returned true,
	// or when the user forced this driver as an override.
	Plan(dir string) ServePlan
}

// Rewrite is an internal path rewrite applied before try_files, e.g. for
// WordPress multisite subdirectory installs.
type Rewrite struct {
	// Match is an RE2 regular expression matched against the request path.
	Match string `json:"match"`
	// Replace is the new path; may reference capture groups as $1, $2...
	Replace string `json:"replace"`
	// IfNotExists limits the rewrite to requests whose current path is not
	// an existing file or directory under the docroot (nginx's
	// "if (!-e $request_filename)"). Without it, multisite rules would also
	// rewrite real core files such as /wp-admin/edit.php.
	IfNotExists bool `json:"ifNotExists,omitempty"`
}

// ServePlan is the server-agnostic description of how to serve a project.
// The proxy package translates it into Caddy configuration.
type ServePlan struct {
	// Driver is the Name() of the driver that produced this plan.
	Driver string `json:"driver"`
	// Docroot is the absolute directory files are served from.
	Docroot string `json:"docroot"`
	// PHP reports whether .php files must be passed to a PHP pool.
	PHP bool `json:"php"`
	// FrontController is the docroot-relative script that receives requests
	// that match no file, e.g. "/index.php". Empty for static sites.
	FrontController string `json:"frontController,omitempty"`
	// TryFiles is the ordered list of candidates, using the tokens {path}
	// (request path) and {query} (raw query string), e.g.
	// ["{path}", "{path}/", "/index.php?{query}"]. A trailing "/" means
	// "an existing directory" (served with IndexFiles). An entry of the form
	// "=404" is a status-code fallback, as in Caddy's try_files.
	TryFiles []string `json:"tryFiles"`
	// IndexFiles are tried for directory requests, e.g. ["index.php", "index.html"].
	IndexFiles []string `json:"indexFiles"`
	// Deny lists request-path patterns that must answer 404, using
	// Caddy-style path matchers ("/.env", "/.git/*", "*.sql"...). Patterns
	// are lowercase because Caddy's path matcher lowercases the request path.
	Deny []string `json:"deny"`
	// Allow lists request-path patterns (same syntax as Deny) that are exempt
	// from Deny, e.g. "/.well-known/*" while every other dotfile is denied.
	Allow []string `json:"allow,omitempty"`
	// Rewrites are applied in order before TryFiles, each to the result of
	// the previous one. Drivers write them so that this gives the same
	// result as stopping at the first match (nginx "rewrite ... last").
	Rewrites []Rewrite `json:"rewrites,omitempty"`
}
