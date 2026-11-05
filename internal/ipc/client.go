package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/platform"
)

// maxErrorBody bounds how much of a non-2xx body is read.
const maxErrorBody = 1 << 20

// maxWaitReconnects bounds how often WaitJob resubscribes after the daemon
// ends the event stream without delivering anything new.
const maxWaitReconnects = 3

// Client talks to the daemon over its IPC address. It is safe for concurrent
// use. The zero value is not usable; call NewClient.
type Client struct {
	addr string
	hc   *http.Client
}

// unreachableError is returned by the transport's dialer; it matches both
// ErrUnreachable and the underlying dial error with errors.Is/As.
type unreachableError struct {
	addr  string
	cause error
}

func (e *unreachableError) Error() string {
	return fmt.Sprintf("%s at %s: %v", ErrUnreachable, e.addr, e.cause)
}

func (e *unreachableError) Unwrap() []error { return []error{ErrUnreachable, e.cause} }

// NewClient returns a client for the daemon listening on addr.
func NewClient(addr string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := platform.DialIPC(ctx, addr)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, &unreachableError{addr: addr, cause: err}
			}
			return conn, nil
		},
		DisableCompression: true,
		MaxIdleConns:       4,
		ForceAttemptHTTP2:  false,
	}
	return &Client{addr: addr, hc: &http.Client{Transport: tr}}
}

// Addr returns the IPC address the client dials.
func (c *Client) Addr() string { return c.addr }

func (c *Client) newRequest(ctx context.Context, method, path string, in any) (*http.Request, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("encode %s %s request: %w", method, path, err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+host+path, body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// transportError normalises an error from http.Client.Do: dial failures come
// back as the *unreachableError itself, context errors as ctx.Err().
func transportError(ctx context.Context, method, path string, err error) error {
	var ue *unreachableError
	if errors.As(err, &ue) {
		return ue
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%s %s: %w", method, path, err)
}

// decodeError turns a non-2xx response into *api.Error.
func decodeError(resp *http.Response) *api.Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var er api.ErrorResponse
	if err := json.Unmarshal(body, &er); err == nil && er.Error.Message != "" {
		if er.Error.Code == "" {
			er.Error.Code = codeForStatus(resp.StatusCode)
		}
		return &er.Error
	}
	return &api.Error{
		Code:    codeForStatus(resp.StatusCode),
		Message: fmt.Sprintf("daemon returned HTTP %d", resp.StatusCode),
		Hint:    "check the daemon log; the client and daemon versions may differ",
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return api.CodeBadRequest
	case http.StatusNotFound:
		return api.CodeNotFound
	case http.StatusConflict:
		return api.CodeConflict
	default:
		return api.CodeInternal
	}
}

// Do sends a JSON request and decodes a JSON response into out (if non-nil).
// path is a full path such as api.Prefix+"/status". A non-2xx response is
// returned as *api.Error; a dial failure wraps ErrUnreachable.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	req, err := c.newRequest(ctx, method, path, in)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return transportError(ctx, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("decode response to %s %s: %w (the client and daemon versions may differ)", method, path, err)
	}
	return nil
}

// Events opens the SSE stream. lastID > 0 is sent as Last-Event-ID so the
// daemon replays buffered events after it. The request is made before Events
// returns, so once the daemon has subscribed and sent its headers no later
// event is missed.
//
// The event channel is closed when the stream ends; then exactly one value
// is sent on the error channel (nil when the daemon ended the stream cleanly,
// ctx.Err() on cancellation) and it is closed. There is no reconnect here.
func (c *Client) Events(ctx context.Context, lastID uint64) (<-chan api.Event, <-chan error) {
	evs := make(chan api.Event, 16)
	errc := make(chan error, 1)
	fail := func(err error) (<-chan api.Event, <-chan error) {
		close(evs)
		errc <- err
		close(errc)
		return evs, errc
	}

	path := api.Prefix + "/events"
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fail(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(lastID, 10))
	}
	resp, err := c.hc.Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		return fail(transportError(ctx, http.MethodGet, path, err))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := decodeError(resp)
		_ = resp.Body.Close()
		return fail(apiErr)
	}

	go func() {
		defer func() { _ = resp.Body.Close() }()
		finish := func(err error) {
			close(evs)
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			errc <- err
			close(errc)
		}
		r := newSSEReader(resp.Body)
		for {
			ev, err := r.next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				} else {
					err = fmt.Errorf("read event stream: %w", err)
				}
				finish(err)
				return
			}
			select {
			case evs <- ev:
			case <-ctx.Done():
				finish(nil)
				return
			}
		}
	}()
	return evs, errc
}

// Certs returns GET /v1/certs.
func (c *Client) Certs(ctx context.Context) (api.Certs, error) {
	var out api.Certs
	err := c.Do(ctx, http.MethodGet, api.Prefix+"/certs", nil, &out)
	return out, err
}

// ExportCert copies the local CA root. An empty path returns the PEM only.
func (c *Client) ExportCert(ctx context.Context, path string) (api.CertExport, error) {
	var out api.CertExport
	err := c.Do(ctx, http.MethodPost, api.Prefix+"/certs/export", api.CertExportRequest{Path: path}, &out)
	return out, err
}

// Status returns GET /v1/status.
func (c *Client) Status(ctx context.Context) (api.Status, error) {
	var s api.Status
	err := c.Do(ctx, http.MethodGet, api.Prefix+"/status", nil, &s)
	return s, err
}

// Services returns the supervised services.
func (c *Client) Services(ctx context.Context) ([]api.Service, error) {
	var r api.ServicesResponse
	if err := c.Do(ctx, http.MethodGet, api.Prefix+"/services", nil, &r); err != nil {
		return nil, err
	}
	return r.Services, nil
}

// ServiceAction starts, stops or restarts a service (or api.ServiceAll).
func (c *Client) ServiceAction(ctx context.Context, name, action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("unknown service action %q: use start, stop or restart", action)
	}
	if name == "" {
		return errors.New("service name is empty: pass a service name or " + strconv.Quote(api.ServiceAll))
	}
	return c.Do(ctx, http.MethodPost, api.Prefix+"/services/"+url.PathEscape(name)+"/"+action, nil, nil)
}

// ServiceLogs returns the last tail lines of a service's log (tail <= 0
// leaves the default to the daemon).
func (c *Client) ServiceLogs(ctx context.Context, name string, tail int) (api.LogsResponse, error) {
	path := api.Prefix + "/services/" + url.PathEscape(name) + "/logs"
	if tail > 0 {
		path += "?tail=" + strconv.Itoa(tail)
	}
	var r api.LogsResponse
	err := c.Do(ctx, http.MethodGet, path, nil, &r)
	return r, err
}

// Jobs lists known jobs.
func (c *Client) Jobs(ctx context.Context) ([]api.Job, error) {
	var r api.JobsResponse
	if err := c.Do(ctx, http.MethodGet, api.Prefix+"/jobs", nil, &r); err != nil {
		return nil, err
	}
	return r.Jobs, nil
}

// Job returns one job.
func (c *Client) Job(ctx context.Context, id string) (api.Job, error) {
	var j api.Job
	err := c.Do(ctx, http.MethodGet, api.Prefix+"/jobs/"+url.PathEscape(id), nil, &j)
	return j, err
}

// CancelJob asks the daemon to cancel a job.
func (c *Client) CancelJob(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodPost, api.Prefix+"/jobs/"+url.PathEscape(id)+"/cancel", nil, nil)
}

// Settings returns all settings.
func (c *Client) Settings(ctx context.Context) (api.Settings, error) {
	var s api.Settings
	err := c.Do(ctx, http.MethodGet, api.Prefix+"/settings", nil, &s)
	return s, err
}

// PatchSettings updates the given keys and returns the resulting settings.
func (c *Client) PatchSettings(ctx context.Context, s api.Settings) (api.Settings, error) {
	var out api.Settings
	err := c.Do(ctx, http.MethodPatch, api.Prefix+"/settings", s, &out)
	return out, err
}

// WaitJob blocks until job id reaches a terminal status and returns it.
// onProgress (may be nil) is called for each job.progress event of the job.
//
// It subscribes to events first and then polls the job once, so a job that
// finishes between the two is not missed. If the daemon ends the stream
// (e.g. the subscriber fell behind) it resubscribes from the last event seen
// and polls again.
func (c *Client) WaitJob(ctx context.Context, id string, onProgress func(api.JobProgressEvent)) (api.Job, error) {
	var lastID uint64
	idle := 0
	for {
		sctx, cancel := context.WithCancel(ctx)
		evs, errc := c.Events(sctx, lastID)
		j, err := c.Job(ctx, id)
		if err != nil {
			cancel()
			return api.Job{}, err
		}
		if j.Status.Done() {
			cancel()
			return j, nil
		}

		got := false
		for ev := range evs {
			got = true
			lastID = ev.ID
			switch ev.Type {
			case api.EventJobProgress:
				var p api.JobProgressEvent
				if json.Unmarshal(ev.Data, &p) == nil && p.JobID == id && onProgress != nil {
					onProgress(p)
				}
			case api.EventJobDone:
				var d api.JobDoneEvent
				if json.Unmarshal(ev.Data, &d) == nil && d.Job.ID == id {
					cancel()
					return d.Job, nil
				}
			}
		}
		serr := <-errc
		cancel()
		if ctx.Err() != nil {
			return api.Job{}, ctx.Err()
		}
		if serr != nil {
			return api.Job{}, fmt.Errorf("wait for job %s: %w", id, serr)
		}
		if got {
			idle = 0
		} else if idle++; idle >= maxWaitReconnects {
			return api.Job{}, fmt.Errorf("wait for job %s: the daemon keeps closing the event stream (check the daemon log)", id)
		}
	}
}
