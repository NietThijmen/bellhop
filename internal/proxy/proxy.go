// Package proxy embeds Caddy and serves sites over HTTP and HTTPS from each
// site's drivers.ServePlan.
//
// The default bind is 127.0.0.1 and ::1 on ports 80 and 443. If either
// privileged port fails, both fall back to 8080 and 8443 and Degraded is
// set. An explicit Options.Addr is one HTTP port; HTTPS uses the next port.
// The local CA is named bellhop and is not installed into the trust store.
// The daemon should:
//
//	p := proxy.New(proxy.Options{
//		LogsDir: dirs.Logs,
//		OnReloaded: func(port int) { bus.Publish(api.EventProxyReloaded, map[string]any{"port": port}) },
//		OnError:    func(err error) { bus.Publish(api.EventProxyError, map[string]string{"message": err.Error()}) },
//	})
//	if err := p.Start(ctx); err != nil { /* listener did not come up */ }
//	port := p.Port() // use this in http://<name>.localhost:<port>
//	// whenever the site list changes:
//	err := p.Reload(ctx, sites) // err.Error() includes Caddy's message; OnError is called with the same error
//
// Reload with the full list (not a delta). On failure the last config that
// loaded is loaded again. Wire either the returned error or OnError, not both,
// or proxy.error is published twice. Callbacks run on the caller's goroutine
// and must not call Start, Reload, or Close.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"                      // http app, matchers, static_response, vars, subroute
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"           // file_server and the file matcher (try_files)
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"              // registered for later response header rules
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/requestbody"          // request_body max_size
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy/fastcgi" // reverse_proxy handler and fastcgi transport
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"              // rewrite handler
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"                       // local CA, install_trust false
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"                       // http app provisions the tls app
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"                    // default Caddy storage module
	_ "github.com/caddyserver/caddy/v2/modules/logging"                        // access log file writer and json encoder
)

// DefaultAddr is the HTTP fallback when ports 80 and 443 are unavailable.
const DefaultAddr = "127.0.0.1:8080"

// Privileged and fallback ports. HTTPS is the next port after an explicit Addr.
const (
	privilegedHTTP  = 80
	privilegedHTTPS = 443
	fallbackHTTP    = 8080
	fallbackHTTPS   = 8443
)

// DefaultMaxBody is the request body limit installed on every site (512 MiB).
const DefaultMaxBody int64 = 512 << 20

// Options configures a Proxy. Zero values are usable.
type Options struct {
	// Addr is an explicit HTTP listen address. Empty tries ports 80 and 443
	// on the loopback interfaces, then 8080 and 8443.
	// When set, HTTPS listens on the next port of the same host.
	Addr string
	// LogsDir is the directory whose sites/<name>.log files receive
	// per-site access logs. Empty skips access logging.
	LogsDir string
	// StorageDir is Caddy's file_system storage root. Empty leaves Caddy's
	// default, which is outside Bellhop's data directory.
	StorageDir string
	// MaxBody is the request body limit in bytes. <= 0 means DefaultMaxBody.
	MaxBody int64
	// OnReloaded is called after a config loads, with the port that is listening.
	OnReloaded func(port int)
	// OnError is called when Start or Reload fails, with the same error
	// returned to the caller. The error text includes Caddy's message.
	OnError func(err error)
}

// Proxy is one embedded Caddy process. A process should run one Proxy;
// Caddy's loaded config is global.
type Proxy struct {
	opts      Options
	mu        sync.Mutex
	host      string
	port      int
	httpsPort int
	degraded  bool
	started   bool
	last      []byte
}

// New returns a Proxy that is not listening yet.
func New(opts Options) *Proxy {
	return &Proxy{opts: opts}
}

// Start binds the listener and loads a config with no sites.
// The first Reload also binds, so Start is only needed when the
// listener should be up before any site exists.
func (p *Proxy) Start(ctx context.Context) error {
	return p.Reload(ctx, nil)
}

// Reload replaces the running config with sites.
// The first successful call records the port returned by Port.
// If the new config does not load, the last good config is loaded
// again and the error (including Caddy's message) is returned.
func (p *Proxy) Reload(ctx context.Context, sites []Site) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	err := p.reload(ctx, sites)
	port := p.port
	onOK := p.opts.OnReloaded
	onErr := p.opts.OnError
	p.mu.Unlock()
	if err != nil {
		if onErr != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			onErr(err)
		}
		return err
	}
	if onOK != nil {
		onOK(port)
	}
	return nil
}

// Port returns the HTTP port Caddy is listening on, or 0 before a
// successful Start or Reload.
func (p *Proxy) Port() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port
}

// HTTPSPort returns the HTTPS port, or 0 before a successful Start or Reload.
func (p *Proxy) HTTPSPort() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.httpsPort
}

// Degraded reports that the privileged ports were unavailable and the
// proxy is serving 8080 and 8443 instead.
func (p *Proxy) Degraded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.degraded
}

// Close stops Caddy and forgets the chosen port. It is safe to call
// more than once.
func (p *Proxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	host, port, httpsPort := p.host, p.port, p.httpsPort
	if err := caddy.Stop(); err != nil {
		return fmt.Errorf("stop proxy: %w", err)
	}
	// Stop does not wait for the listener to close unless the process
	// is exiting (caddyhttp.App.stop). Poll so Close means the port is free.
	if err := waitClosed(host, port); err != nil {
		return err
	}
	if err := waitClosed(host, httpsPort); err != nil {
		return err
	}
	p.started = false
	p.port = 0
	p.httpsPort = 0
	p.degraded = false
	p.host = ""
	p.last = nil
	return nil
}

func waitClosed(host string, port int) error {
	if port == 0 {
		return nil
	}
	if host == "" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return nil
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("listener %s still open after stop", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type listenPlan struct {
	host      string
	httpPort  int
	httpsPort int
	http      []string
	https     []string
	degraded  bool
}

func (p *Proxy) reload(ctx context.Context, sites []Site) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plans, err := p.listenPlans()
	if err != nil {
		return err
	}
	if p.port != 0 {
		v6 := p.host == "" && ipv6Loopback()
		plans = []listenPlan{{
			host:      p.host,
			httpPort:  p.port,
			httpsPort: p.httpsPort,
			http:      addresses(p.host, p.port, v6),
			https:     addresses(p.host, p.httpsPort, v6),
			degraded:  p.degraded,
		}}
	}

	var (
		cfg     []byte
		loadErr error
		chosen  *listenPlan
	)
	for i := range plans {
		if err := ctx.Err(); err != nil {
			return err
		}
		plan := plans[i]
		cfg, err = Build(Input{
			HTTPListen:  plan.http,
			HTTPSListen: plan.https,
			HTTPSPort:   plan.httpsPort,
			LogsDir:     p.opts.LogsDir,
			StorageDir:  p.opts.StorageDir,
			MaxBody:     p.opts.MaxBody,
			Sites:       sites,
		})
		if err != nil {
			return err
		}
		loadErr = caddy.Load(cfg, true)
		if loadErr == nil {
			chosen = &plans[i]
			break
		}
		if p.port != 0 || !isBindError(loadErr) {
			break
		}
	}
	if chosen == nil {
		if loadErr == nil {
			loadErr = fmt.Errorf("no free TCP port for the proxy")
		}
		if revErr := p.restore(); revErr != nil && !isBindError(revErr) {
			return fmt.Errorf("proxy reload: %w; restore last config: %w", loadErr, revErr)
		}
		return fmt.Errorf("proxy reload: %w", loadErr)
	}
	p.host = chosen.host
	p.port = chosen.httpPort
	p.httpsPort = chosen.httpsPort
	p.degraded = chosen.degraded
	p.started = true
	p.last = cfg
	return nil
}

func (p *Proxy) listenPlans() ([]listenPlan, error) {
	if strings.TrimSpace(p.opts.Addr) != "" {
		host, port, err := parseAddr(p.opts.Addr)
		if err != nil {
			return nil, err
		}
		if port == 65535 {
			return nil, fmt.Errorf("proxy listen address %q: no following port for HTTPS", p.opts.Addr)
		}
		return []listenPlan{{
			host:      host,
			httpPort:  port,
			httpsPort: port + 1,
			http:      []string{net.JoinHostPort(host, strconv.Itoa(port))},
			https:     []string{net.JoinHostPort(host, strconv.Itoa(port+1))},
		}}, nil
	}
	v6 := ipv6Loopback()
	return []listenPlan{
		{
			httpPort: privilegedHTTP, httpsPort: privilegedHTTPS,
			http: addresses("", privilegedHTTP, v6), https: addresses("", privilegedHTTPS, v6),
		},
		{
			httpPort: fallbackHTTP, httpsPort: fallbackHTTPS, degraded: true,
			http: addresses("", fallbackHTTP, v6), https: addresses("", fallbackHTTPS, v6),
		},
	}, nil
}

func addresses(host string, port int, ipv6 bool) []string {
	if host != "" {
		return []string{net.JoinHostPort(host, strconv.Itoa(port))}
	}
	out := []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	if ipv6 {
		out = append(out, net.JoinHostPort("::1", strconv.Itoa(port)))
	}
	return out
}

func ipv6Loopback() bool {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// restore puts the last good config back. Caddy keeps that config running
// when a new one fails to provision, so a second load can fail with a bind
// error while the previous listener is still up. That bind error means the
// last good config is already serving.
func (p *Proxy) restore() error {
	if len(p.last) == 0 {
		return nil
	}
	return caddy.Load(p.last, true)
}

func parseAddr(addr string) (string, int, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("proxy listen address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("proxy listen address %q: port must be between 1 and 65535", addr)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host, port, nil
}

func isBindError(err error) bool {
	if err == nil {
		return false
	}
	// Caddy wraps the listen error with %v (caddyhttp.App.Start), so the
	// net.OpError is not in the chain. These substrings are the Go messages
	// on Windows and Unix.
	msg := err.Error()
	return strings.Contains(msg, "bind:") ||
		strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "Only one usage of each socket address")
}
