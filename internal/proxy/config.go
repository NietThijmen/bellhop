package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nietthijmen/bellhop/internal/drivers"
)

// Site is one project to route. The daemon fills it from sites.Site plus
// the PHP pool address for that site's version. Socket is ignored when
// Plan.PHP is false. PlanErr (or an empty docroot) serves an error page
// on Hosts instead of dropping the site.
type Site struct {
	Name    string
	Hosts   []string // primary hostname, wildcard, aliases; port is not included
	Plan    drivers.ServePlan
	PlanErr error
	// Secure redirects HTTP to HTTPS and serves the site on the TLS listener.
	Secure bool
	// Socket is one PHP pool address. "127.0.0.1:9000",
	// "unix//run/php-8.3.sock", "unix/C:/bellhop/run/php-8.3.sock", or an
	// absolute filesystem path (rewritten to Caddy's unix/ dial form).
	// Ignored when Sockets is non-empty.
	Socket string
	// Sockets are the PHP pool addresses. One entry is a single upstream.
	// Two or more are load-balanced with least_conn
	// (reverseproxy.go load_balancing.selection_policy, inline key policy,
	// selectionpolicies.go least_conn).
	Sockets []string
}

// Input is the Caddy config Build encodes.
// Listen is one HTTP address used when HTTPListen is empty.
// HTTPListen and HTTPSListen are the full listen lists. HTTPSListen
// turns on the local CA and TLS. An empty Listen and HTTPListen means DefaultAddr.
type Input struct {
	Listen      string
	HTTPListen  []string
	HTTPSListen []string
	HTTPSPort   int // port written into HTTP redirects; 443 omits it
	LogsDir     string
	StorageDir  string
	MaxBody     int64
	Sites       []Site
}

// Build returns indented Caddy JSON for in. It does not listen and does
// not create LogsDir. Keys match caddyserver/caddy/v2 v2.11.7:
//
//	admin.disabled, admin.config.persist
//	apps.http.servers.*.listen
//	apps.http.servers.*.automatic_https.disable          (autohttps.go)
//	apps.http.servers.*.protocols                        (server.go; HTTP is "h1", HTTPS is "h1" and "h2", no HTTP/3)
//	apps.http.servers.*.tls_connection_policies          (server.go; non-empty enables TLS)
//	apps.tls.certificates.automate                       (tls.go AutomateLoader)
//	apps.tls.automation.policies.issuers                 (internalissuer.go; inline key module, ca bellhop)
//	apps.pki.certificate_authorities.bellhop             (pki.go map key is the CA id; ca.go name and install_trust)
//	apps.http.servers.*.logs.logger_names                (logging.go)
//	apps.http.servers.*.logs.skip_unmapped_hosts
//	apps.http.servers.*.routes[].match / handle / terminal / group
//	http.handlers.request_body  max_size                 (requestbody.go)
//	http.handlers.vars          root
//	http.handlers.static_response status_code
//	http.handlers.rewrite       uri
//	http.handlers.subroute      routes
//	http.handlers.file_server   root, index_names
//	http.handlers.reverse_proxy upstreams[].dial, transport, load_balancing
//	http.reverse_proxy.load_balancing.selection_policy.policy least_conn (two or more upstreams)
//	http.reverse_proxy.transport.fastcgi protocol, root, split_path, env
//	http.matchers.host, path, path_regexp (name, pattern), not, file.try_files, file.root, file.split_path
//	logging.logs.<name>.include, writer.output "file", writer.filename, encoder.format "json"
//
// HTTPS is not an env override. fastcgi.go sets HTTPS=on only when the
// request is TLS, and writes SERVER_NAME from the Host header. M2 is plain
// HTTP, so HTTPS stays unset. SERVER_NAME is also set in env to the host
// placeholder so the value is visible in the config. A trailing-slash
// try_files entry is expanded to each IndexFiles name first, so a PHP
// index is rewritten to *.php and reaches reverse_proxy instead of being
// served as a static file.
func Build(in Input) ([]byte, error) {
	doc, err := document(in)
	if err != nil {
		return nil, err
	}
	return marshal(doc)
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func document(in Input) (map[string]any, error) {
	httpListen := in.HTTPListen
	if len(httpListen) == 0 {
		listen := strings.TrimSpace(in.Listen)
		if listen == "" {
			listen = DefaultAddr
		}
		httpListen = []string{listen}
	}
	httpsListen := append([]string(nil), in.HTTPSListen...)
	sites := append([]Site(nil), in.Sites...)
	sort.Slice(sites, func(i, j int) bool { return sites[i].Name < sites[j].Name })
	if err := validateSites(sites); err != nil {
		return nil, err
	}

	limit := requestBodyRoute(bodyLimit(in.MaxBody))
	notFound := map[string]any{
		"handle":   []any{staticResponse(http.StatusNotFound, "no such site\n")},
		"terminal": true,
	}
	httpRoutes := []any{limit}
	httpsRoutes := []any{limit}
	var logs []accessLog
	var subjects []string
	for _, site := range sites {
		route, log, err := oneSite(site, in.LogsDir)
		if err != nil {
			return nil, err
		}
		if log.file != "" {
			logs = append(logs, log)
		}
		if site.Secure && len(httpsListen) > 0 {
			hosts, err := normalizeHosts(site.Hosts)
			if err != nil {
				return nil, fmt.Errorf("site %s: %w", site.Name, err)
			}
			httpRoutes = append(httpRoutes, redirectRoute(hosts, in.HTTPSPort))
			httpsRoutes = append(httpsRoutes, route)
			subjects = append(subjects, hosts...)
		} else {
			httpRoutes = append(httpRoutes, route)
		}
	}
	httpRoutes = append(httpRoutes, notFound)
	httpsRoutes = append(httpsRoutes, notFound)

	httpName := "sites"
	if len(httpsListen) > 0 {
		httpName = "http"
	}
	servers := map[string]any{
		httpName: httpServer(httpListen, httpRoutes, logs),
	}
	if len(httpsListen) > 0 {
		https := httpServer(httpsListen, httpsRoutes, logs)
		https["protocols"] = []string{"h1", "h2"}
		// A non-empty policy list enables TLS (caddyhttp app.go useTLS).
		https["tls_connection_policies"] = []any{map[string]any{}}
		servers["https"] = https
	}
	apps := map[string]any{
		"http": map[string]any{"servers": servers},
	}
	if len(httpsListen) > 0 {
		apps["pki"] = pkiApp()
		apps["tls"] = tlsApp(subjects)
	}
	doc := map[string]any{
		"admin": map[string]any{
			"config":   map[string]any{"persist": false},
			"disabled": true,
		},
		"apps": apps,
	}
	if len(logs) > 0 {
		doc["logging"] = map[string]any{"logs": loggingLogs(logs)}
	}
	if dir := strings.TrimSpace(in.StorageDir); dir != "" {
		// caddy.storage.file_system (modules/filestorage): module name file_system, field root.
		doc["storage"] = map[string]any{"module": "file_system", "root": dir}
	}
	return doc, nil
}

func httpServer(listen []string, routes []any, logs []accessLog) map[string]any {
	server := map[string]any{
		"automatic_https": map[string]any{"disable": true},
		"listen":          listen,
		"protocols":       []string{"h1"},
		"routes":          routes,
	}
	if len(logs) > 0 {
		server["logs"] = serverLogs(logs)
	}
	return server
}

// pkiApp is the local CA. The map key is the CA id Caddy stores under
// pki/authorities/<id>. install_trust false keeps trust out of the proxy;
// the helper installs it. The "local" CA is Caddy's implicit default
// (pki.go GetCA); it is declared here so its trust install stays off too.
func pkiApp() map[string]any {
	disabled := map[string]any{"install_trust": false}
	return map[string]any{
		"certificate_authorities": map[string]any{
			"bellhop": map[string]any{
				"name":          "bellhop",
				"install_trust": false,
			},
			"local": disabled,
		},
	}
}

// tlsApp asks the internal issuer (CA id bellhop) for each secure hostname.
func tlsApp(subjects []string) map[string]any {
	app := map[string]any{}
	if len(subjects) == 0 {
		return app
	}
	app["certificates"] = map[string]any{"automate": subjects}
	app["automation"] = map[string]any{
		"policies": []any{map[string]any{
			"subjects": subjects,
			"issuers": []any{map[string]any{
				"module": "internal",
				"ca":     "bellhop",
			}},
		}},
	}
	return app
}

func redirectRoute(hosts []string, httpsPort int) map[string]any {
	loc := "https://{http.request.host}{http.request.uri}"
	if httpsPort != 0 && httpsPort != 443 {
		loc = "https://{http.request.host}:" + strconv.Itoa(httpsPort) + "{http.request.uri}"
	}
	return hostRoute(hosts, []any{map[string]any{
		"handler":     "static_response",
		"status_code": http.StatusPermanentRedirect,
		"headers":     map[string][]string{"Location": {loc}},
	}}, true)
}

func bodyLimit(n int64) int64 {
	if n <= 0 {
		return DefaultMaxBody
	}
	return n
}

type accessLog struct {
	name  string
	hosts []string
	file  string
}

func validateSites(sites []Site) error {
	names := map[string]struct{}{}
	hosts := map[string]string{}
	for _, site := range sites {
		if err := validateName(site.Name); err != nil {
			return err
		}
		key := strings.ToLower(site.Name)
		if _, ok := names[key]; ok {
			return fmt.Errorf("duplicate site name %q", site.Name)
		}
		names[key] = struct{}{}
		list, err := normalizeHosts(site.Hosts)
		if err != nil {
			return fmt.Errorf("site %s: %w", site.Name, err)
		}
		for _, h := range list {
			hk := strings.ToLower(h)
			if other, ok := hosts[hk]; ok {
				return fmt.Errorf("host %q is used by both %s and %s", h, other, site.Name)
			}
			hosts[hk] = site.Name
		}
	}
	return nil
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid site name %q", name)
	}
	return nil
}

func normalizeHosts(hosts []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		k := strings.ToLower(h)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil, errors.New("no hostnames")
	}
	return out, nil
}

func oneSite(site Site, logsDir string) (map[string]any, accessLog, error) {
	hosts, err := normalizeHosts(site.Hosts)
	if err != nil {
		return nil, accessLog{}, fmt.Errorf("site %s: %w", site.Name, err)
	}
	var log accessLog
	if logsDir != "" {
		log = accessLog{
			name:  site.Name,
			hosts: hosts,
			file:  filepath.ToSlash(filepath.Join(logsDir, "sites", site.Name+".log")),
		}
	}
	if site.PlanErr != nil || strings.TrimSpace(site.Plan.Docroot) == "" {
		msg := "no docroot"
		if site.PlanErr != nil {
			msg = site.PlanErr.Error()
		}
		return hostRoute(hosts, []any{staticResponse(http.StatusInternalServerError, msg+"\n")}, true), log, nil
	}

	docroot := site.Plan.Docroot
	inner := []any{varsRoute(docroot)}
	if deny := denyRoute(site.Plan); deny != nil {
		inner = append(inner, deny)
	}
	inner = append(inner, rewriteRoutes(site.Name, docroot, site.Plan.Rewrites)...)
	inner = append(inner, tryRoutes(site.Name, docroot, site.Plan)...)
	if site.Plan.PHP {
		dials, err := phpDials(site)
		if err != nil {
			return nil, accessLog{}, fmt.Errorf("site %s: %w", site.Name, err)
		}
		inner = append(inner, phpRoute(docroot, dials))
	}
	inner = append(inner, fileServerRoute(docroot, site.Plan.IndexFiles))
	route := hostRoute(hosts, []any{map[string]any{
		"handler": "subroute",
		"routes":  inner,
	}}, true)
	return route, log, nil
}

func hostRoute(hosts []string, handle []any, terminal bool) map[string]any {
	route := map[string]any{
		"handle": handle,
		"match":  []any{map[string]any{"host": hosts}},
	}
	if terminal {
		route["terminal"] = true
	}
	return route
}

func staticResponse(status int, body string) map[string]any {
	h := map[string]any{
		"handler":     "static_response",
		"status_code": status,
	}
	if body != "" {
		h["body"] = body
	}
	return h
}

func requestBodyRoute(n int64) map[string]any {
	return map[string]any{
		"handle": []any{map[string]any{
			"handler":  "request_body",
			"max_size": n,
		}},
	}
}

func varsRoute(docroot string) map[string]any {
	return map[string]any{
		"handle": []any{map[string]any{
			"handler": "vars",
			"root":    docroot,
		}},
	}
}

func denyRoute(plan drivers.ServePlan) map[string]any {
	if len(plan.Deny) == 0 {
		return nil
	}
	match := map[string]any{"path": append([]string(nil), plan.Deny...)}
	if len(plan.Allow) > 0 {
		match["not"] = []any{map[string]any{"path": append([]string(nil), plan.Allow...)}}
	}
	return map[string]any{
		"handle":   []any{staticResponse(http.StatusNotFound, "")},
		"match":    []any{match},
		"terminal": true,
	}
}

func rewriteRoutes(site, docroot string, rules []drivers.Rewrite) []any {
	out := make([]any, 0, len(rules))
	// One route group: Caddy runs only the first matching route of a group,
	// which gives nginx "rewrite ... last" semantics. Without it a rewritten
	// path could be rewritten again by a later rule.
	group := "rw_" + token(site)
	for i, rw := range rules {
		name := regexpName(site, i)
		match := map[string]any{
			"path_regexp": map[string]any{
				"name":    name,
				"pattern": rw.Match,
			},
		}
		if rw.IfNotExists {
			match["not"] = []any{map[string]any{
				"file": map[string]any{
					"root": docroot,
					"try_files": []string{
						"{http.request.uri.path}",
						"{http.request.uri.path}/",
					},
				},
			}}
		}
		out = append(out, map[string]any{
			"group": group,
			"handle": []any{map[string]any{
				"handler": "rewrite",
				"uri":     rewriteTarget(name, rw.Replace),
			}},
			"match": []any{match},
		})
	}
	return out
}

func tryRoutes(site, docroot string, plan drivers.ServePlan) []any {
	batches := batchTryFiles(expandTryFiles(plan.TryFiles, plan.IndexFiles))
	if len(batches) == 0 {
		return nil
	}
	group := "try_" + token(site)
	out := make([]any, 0, len(batches))
	for _, batch := range batches {
		file := map[string]any{
			"root":      docroot,
			"try_files": batch.files,
		}
		if plan.PHP {
			file["split_path"] = []string{".php"}
		}
		uri := "{http.matchers.file.relative}" + batch.query
		out = append(out, map[string]any{
			"group": group,
			"handle": []any{map[string]any{
				"handler": "rewrite",
				"uri":     uri,
			}},
			"match": []any{map[string]any{"file": file}},
		})
	}
	return out
}

func phpRoute(docroot string, dials []string) map[string]any {
	upstreams := make([]any, len(dials))
	for i, dial := range dials {
		upstreams[i] = map[string]any{"dial": dial}
	}
	handler := map[string]any{
		"handler": "reverse_proxy",
		"transport": map[string]any{
			"env": map[string]string{
				"SERVER_NAME": "{http.request.host}",
			},
			"protocol":   "fastcgi",
			"root":       docroot,
			"split_path": []string{".php"},
		},
		"upstreams": upstreams,
	}
	if len(dials) >= 2 {
		// LeastConnSelection has no fields. The module id's last label is
		// the inline policy key (caddy:"inline_key=policy").
		handler["load_balancing"] = map[string]any{
			"selection_policy": map[string]any{"policy": "least_conn"},
		}
	}
	return map[string]any{
		"handle":   []any{handler},
		"match":    []any{map[string]any{"path": []string{"*.php"}}},
		"terminal": true,
	}
}

func fileServerRoute(docroot string, index []string) map[string]any {
	h := map[string]any{
		"handler": "file_server",
		"root":    docroot,
	}
	if len(index) > 0 {
		h["index_names"] = append([]string(nil), index...)
	}
	return map[string]any{"handle": []any{h}}
}

func serverLogs(logs []accessLog) map[string]any {
	names := make(map[string][]string, len(logs))
	for _, l := range logs {
		for _, h := range l.hosts {
			names[h] = []string{l.name}
		}
	}
	return map[string]any{
		"logger_names":        names,
		"skip_unmapped_hosts": true,
	}
}

func loggingLogs(logs []accessLog) map[string]any {
	out := make(map[string]any, len(logs))
	for _, l := range logs {
		out[l.name] = map[string]any{
			"encoder": map[string]any{"format": "json"},
			"include": []string{"http.log.access." + l.name},
			"writer": map[string]any{
				"filename": l.file,
				"output":   "file",
				// filewriter.go enables rolling unless this is false. A rolling
				// writer is a separate process-wide file handle.
				"roll": false,
			},
		}
	}
	return out
}

// expandTryFiles inserts "<dir><index>" before a trailing-slash try_files
// entry. Caddy's file matcher treats a trailing slash as "this directory
// exists"; file_server would then send index.php as bytes. Trying the index
// file first rewrites the URI to that file so reverse_proxy can run it.
// The directory entry stays after the indexes, so a directory with no index
// does not fall through to the front controller.
func expandTryFiles(tryFiles, indexFiles []string) []string {
	indexes := make([]string, 0, len(indexFiles))
	for _, name := range indexFiles {
		name = strings.TrimPrefix(name, "/")
		if name != "" {
			indexes = append(indexes, name)
		}
	}
	out := make([]string, 0, len(tryFiles)+len(indexes))
	for _, entry := range tryFiles {
		if len(indexes) > 0 && strings.HasSuffix(entry, "/") && !strings.Contains(entry, "?") && !strings.HasPrefix(entry, "=") {
			for _, name := range indexes {
				out = append(out, entry+name)
			}
		}
		out = append(out, entry)
	}
	return out
}

type tryBatch struct {
	files []string
	query string // includes the leading "?", or empty
}

// batchTryFiles splits entries the way caddyhttp's try_files directive does:
// a candidate with a query string is its own route, because the rewrite URI
// differs. {path} and {query} are Caddyfile shorthands (httpcaddyfile/shorthands.go).
func batchTryFiles(entries []string) []tryBatch {
	var batches []tryBatch
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		batches = append(batches, tryBatch{files: cur})
		cur = nil
	}
	for _, entry := range entries {
		entry = caddyPlaceholders(entry)
		before, after, ok := strings.Cut(entry, "?")
		if ok {
			flush()
			batches = append(batches, tryBatch{files: []string{before}, query: "?" + after})
			continue
		}
		cur = append(cur, entry)
	}
	flush()
	return batches
}

func caddyPlaceholders(s string) string {
	s = strings.ReplaceAll(s, "{path}", "{http.request.uri.path}")
	s = strings.ReplaceAll(s, "{query}", "{http.request.uri.query}")
	return s
}

var captureRef = regexp.MustCompile(`\$(\d+)`)

func rewriteTarget(name, replace string) string {
	return captureRef.ReplaceAllString(replace, "{http.regexp."+name+".$1}")
}

func regexpName(site string, i int) string {
	return "rw_" + token(site) + "_" + strconv.Itoa(i)
}

func token(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "site"
	}
	return b.String()
}

func phpDials(site Site) ([]string, error) {
	raw := site.Sockets
	if len(raw) == 0 && strings.TrimSpace(site.Socket) != "" {
		raw = []string{site.Socket}
	}
	if len(raw) == 0 {
		return nil, errors.New("PHP socket is empty")
	}
	out := make([]string, 0, len(raw))
	for _, socket := range raw {
		dial, err := phpDial(socket)
		if err != nil {
			return nil, err
		}
		out = append(out, dial)
	}
	return out, nil
}

// phpDial converts a pool address into Caddy's reverse_proxy dial form.
// Absolute paths become "unix/" + slash path, which is "unix//abs" on
// Unix (reverseproxy/addresses_test.go) and "unix/C:/abs" on Windows.
func phpDial(socket string) (string, error) {
	s := strings.TrimSpace(socket)
	switch {
	case s == "":
		return "", errors.New("PHP socket is empty")
	case strings.HasPrefix(s, "unix/"):
		return s, nil
	}
	if host, port, err := net.SplitHostPort(s); err == nil && host != "" && port != "" {
		if _, err := strconv.Atoi(port); err == nil {
			return net.JoinHostPort(host, port), nil
		}
	}
	if filepath.IsAbs(s) {
		return "unix/" + filepath.ToSlash(s), nil
	}
	return "", fmt.Errorf("PHP socket %q must be host:port, a unix/ dial address, or an absolute path", s)
}
