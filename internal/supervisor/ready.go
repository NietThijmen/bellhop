package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	defaultReadyTimeout = 30 * time.Second
	readyPollInterval   = 50 * time.Millisecond
	readyProbeTimeout   = time.Second
)

func (r Readiness) validate() error {
	set := 0
	for _, s := range []string{r.TCPAddr, r.UnixSocket, r.HTTPURL} {
		if s != "" {
			set++
		}
	}
	if set > 1 {
		return errors.New("readiness: set at most one of TCPAddr, UnixSocket and HTTPURL")
	}
	if r.Timeout < 0 {
		return errors.New("readiness: timeout must not be negative")
	}
	return nil
}

func (r Readiness) isZero() bool { return r.TCPAddr == "" && r.UnixSocket == "" && r.HTTPURL == "" }

func (r Readiness) describe() string {
	switch {
	case r.TCPAddr != "":
		return "TCP " + r.TCPAddr
	case r.UnixSocket != "":
		return "socket " + r.UnixSocket
	case r.HTTPURL != "":
		return "HTTP " + r.HTTPURL
	}
	return "start"
}

// waitReady polls the readiness probe until it succeeds, the timeout passes
// or ctx is cancelled.
func waitReady(ctx context.Context, r Readiness) error {
	if r.isZero() {
		return nil
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = defaultReadyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(readyPollInterval)
	defer t.Stop()
	var last error
	for {
		if last = probe(ctx, r); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("not ready on %s within %v: %w", r.describe(), timeout, last)
			}
			return ctx.Err()
		case <-t.C:
		}
	}
}

func probe(ctx context.Context, r Readiness) error {
	pctx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
	defer cancel()
	switch {
	case r.TCPAddr != "":
		return dialProbe(pctx, "tcp", r.TCPAddr)
	case r.UnixSocket != "":
		return dialProbe(pctx, "unix", r.UnixSocket)
	default:
		return httpProbe(pctx, r.HTTPURL)
	}
}

func dialProbe(ctx context.Context, network, addr string) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return err
	}
	return c.Close()
}

var probeClient = &http.Client{
	// Readiness must not follow redirects to somewhere else.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
}

func httpProbe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	return nil
}
