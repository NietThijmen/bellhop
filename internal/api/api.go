// Package api holds every request, response and event type exchanged between
// the daemon and its clients (CLI and GUI). Nothing in here does I/O.
package api

import (
	"encoding/json"
	"time"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// Prefix is the versioned path prefix for every endpoint.
const Prefix = "/v1"

// Routes (relative to the transport root). Path parameters use Go 1.22
// net/http ServeMux syntax so the daemon can register them verbatim.
const (
	RouteStatus = "GET " + Prefix + "/status"
	RouteEvents = "GET " + Prefix + "/events"

	RouteServices       = "GET " + Prefix + "/services"
	RouteServiceStart   = "POST " + Prefix + "/services/{name}/start"
	RouteServiceStop    = "POST " + Prefix + "/services/{name}/stop"
	RouteServiceRestart = "POST " + Prefix + "/services/{name}/restart"
	RouteServiceLogs    = "GET " + Prefix + "/services/{name}/logs"

	RouteJobs      = "GET " + Prefix + "/jobs"
	RouteJob       = "GET " + Prefix + "/jobs/{id}"
	RouteJobCancel = "POST " + Prefix + "/jobs/{id}/cancel"

	RouteSettings      = "GET " + Prefix + "/settings"
	RouteSettingsPatch = "PATCH " + Prefix + "/settings"

	RouteSites     = "GET " + Prefix + "/sites"
	RoutePark      = "POST " + Prefix + "/park"
	RouteUnpark    = "DELETE " + Prefix + "/park" // body: ParkRequest
	RouteParked    = "GET " + Prefix + "/park"
	RouteLink      = "POST " + Prefix + "/link"
	RouteUnlink    = "DELETE " + Prefix + "/link/{name}"
	RouteSitePatch = "PATCH " + Prefix + "/sites/{name}"

	RoutePHP        = "GET " + Prefix + "/php"
	RoutePHPInstall = "POST " + Prefix + "/php/{version}/install" // 202 JobRef
	RoutePHPRemove  = "DELETE " + Prefix + "/php/{version}"
	RoutePHPDefault = "POST " + Prefix + "/php/default" // body: PHPDefaultRequest

	RouteDBInstall = "POST " + Prefix + "/db/install" // 202 JobRef

	RouteCerts       = "GET " + Prefix + "/certs"
	RouteCertsExport = "POST " + Prefix + "/certs/export"
)

// ServiceAll is the pseudo service name meaning "every service" for
// start/stop/restart.
const ServiceAll = "all"

// ---------------------------------------------------------------- errors

// Error codes. Clients map these to exit codes and messages.
const (
	CodeBadRequest = "bad_request"
	CodeNotFound   = "not_found"
	CodeConflict   = "conflict"
	CodeInternal   = "internal"
)

// Error is the body of every non-2xx response: {"error": {...}}.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`        // what failed
	Hint    string `json:"hint,omitempty"` // what to do next
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return e.Message + " (" + e.Hint + ")"
	}
	return e.Message
}

// ErrorResponse wraps Error on the wire.
type ErrorResponse struct {
	Error Error `json:"error"`
}

// ---------------------------------------------------------------- status

// Warning is a non-fatal problem surfaced in status, UI and `bellhop doctor`.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Status is returned by GET /v1/status.
type Status struct {
	Product   string       `json:"product"`
	Build     version.Info `json:"build"`
	PID       int          `json:"pid"`
	StartedAt time.Time    `json:"startedAt"`
	DataDir   string       `json:"dataDir"`
	LogDir    string       `json:"logDir"`
	Degraded  bool         `json:"degraded"`
	Services  []Service    `json:"services"`
	Warnings  []Warning    `json:"warnings"`
}

// ---------------------------------------------------------------- services

// ServiceState is the supervisor lifecycle state of a child service.
type ServiceState string

const (
	StateStopped  ServiceState = "stopped"
	StateStarting ServiceState = "starting"
	StateRunning  ServiceState = "running"
	StateStopping ServiceState = "stopping"
	StateBackoff  ServiceState = "backoff"
	StateFailed   ServiceState = "failed"
)

// Service is a snapshot of one supervised service.
type Service struct {
	Name      string       `json:"name"`
	State     ServiceState `json:"state"`
	PID       int          `json:"pid,omitempty"`
	Port      int          `json:"port,omitempty"`
	Restarts  int          `json:"restarts"`
	Since     time.Time    `json:"since"`
	LastError string       `json:"lastError,omitempty"`
}

// ServicesResponse is returned by GET /v1/services.
type ServicesResponse struct {
	Services []Service `json:"services"`
}

// LogLine is one line of service output.
type LogLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"` // "stdout" | "stderr" | "supervisor"
	Line   string    `json:"line"`
}

// LogsResponse is returned by GET /v1/services/{name}/logs?tail=N.
type LogsResponse struct {
	Service string    `json:"service"`
	Lines   []LogLine `json:"lines"`
}

// ---------------------------------------------------------------- jobs

// JobStatus is the lifecycle of a long-running operation.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Done reports whether the status is terminal.
func (s JobStatus) Done() bool {
	return s == JobSucceeded || s == JobFailed || s == JobCancelled
}

// Job describes a long-running operation (downloads, installs, migrations).
type Job struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Status     JobStatus       `json:"status"`
	Progress   float64         `json:"progress"` // 0..1, or -1 when indeterminate
	Message    string          `json:"message,omitempty"`
	Error      string          `json:"error,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	StartedAt  time.Time       `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	LogPath    string          `json:"logPath,omitempty"`
}

// JobRef is returned by any mutating call that started a job (HTTP 202).
type JobRef struct {
	JobID string `json:"jobId"`
}

// JobsResponse is returned by GET /v1/jobs.
type JobsResponse struct {
	Jobs []Job `json:"jobs"`
}

// ---------------------------------------------------------------- settings

// Settings is a flat key/value map; values are strings on the wire.
type Settings map[string]string

// ---------------------------------------------------------------- sites

// Site sources.
const (
	SourceLinked = "linked"
	SourceParked = "parked"
)

// Site is one served project as clients see it.
type Site struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`   // primary URL, e.g. http://foo.localhost:8080
	Hosts      []string `json:"hosts"` // every hostname routed to it (incl. aliases)
	Path       string   `json:"path"`
	Source     string   `json:"source"` // SourceLinked | SourceParked
	ParkedRoot string   `json:"parkedRoot,omitempty"`
	Driver     string   `json:"driver"` // detected or overridden; "" if nothing matched
	PHP        string   `json:"php"`    // effective PHP version ("" for static sites)
	Isolated   bool     `json:"isolated"`
	Secure     bool     `json:"secure"`
	Docroot    string   `json:"docroot"`
	Aliases    []string `json:"aliases,omitempty"`
}

// SitesResponse is returned by GET /v1/sites.
type SitesResponse struct {
	Sites     []Site    `json:"sites"`
	Conflicts []Warning `json:"conflicts,omitempty"` // name collisions between parked roots
}

// ParkRequest is the body of POST/DELETE /v1/park.
type ParkRequest struct {
	Path string `json:"path"`
}

// ParkedResponse is returned by GET /v1/park.
type ParkedResponse struct {
	Paths []string `json:"paths"`
}

// LinkRequest is the body of POST /v1/link. Name defaults to the
// sanitised directory name.
type LinkRequest struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

// SitePatch is the body of PATCH /v1/sites/{name}. Nil fields are left
// unchanged; an empty string resets PHP/Driver/Docroot to their defaults.
type SitePatch struct {
	PHP     *string   `json:"php,omitempty"`
	Secure  *bool     `json:"secure,omitempty"`
	Driver  *string   `json:"driver,omitempty"`
	Docroot *string   `json:"docroot,omitempty"`
	Aliases *[]string `json:"aliases,omitempty"`
}

// ---------------------------------------------------------------- certs

// Certs describes the local CA root.
type Certs struct {
	CA          string    `json:"ca"`
	Path        string    `json:"path"`
	Fingerprint string    `json:"fingerprint"`
	Expires     time.Time `json:"expires"`
	Trusted     bool      `json:"trusted"`
}

// CertExportRequest is the body of POST /v1/certs/export.
// Path is an absolute file the daemon copies the public root PEM to.
type CertExportRequest struct {
	Path string `json:"path,omitempty"`
}

// CertExport is the response of POST /v1/certs/export.
type CertExport struct {
	Certs
	PEM     string `json:"pem"`
	Written string `json:"written,omitempty"`
}

// ---------------------------------------------------------------- php

// PHPVersion is one PHP version line known to Bellhop.
type PHPVersion struct {
	Version   string `json:"version"`         // major.minor, e.g. "8.3"
	Patch     string `json:"patch,omitempty"` // full version installed or available, e.g. "8.3.27"
	Installed bool   `json:"installed"`
	Default   bool   `json:"default"`
	Path      string `json:"path,omitempty"` // install dir
	InUse     int    `json:"inUse"`          // number of sites using it
	Running   bool   `json:"running"`        // pool is up
}

// PHPResponse is returned by GET /v1/php.
type PHPResponse struct {
	Versions []PHPVersion `json:"versions"`
}

// PHPDefaultRequest is the body of POST /v1/php/default.
type PHPDefaultRequest struct {
	Version string `json:"version"`
}

// ---------------------------------------------------------------- events

// Event names used as the SSE `event:` field.
const (
	EventServiceState  = "service.state"
	EventServiceLog    = "service.log"
	EventJobProgress   = "job.progress"
	EventJobDone       = "job.done"
	EventSitesChanged  = "sites.changed"
	EventProxyReloaded = "proxy.reloaded"
	EventProxyError    = "proxy.error"
	EventMailNew       = "mail.new"
	EventDaemonWarning = "daemon.warning"
)

// Event is one message on the event bus and the SSE stream. On the wire the
// SSE frame is: "id: <ID>\nevent: <Type>\ndata: <json of Event>\n\n".
type Event struct {
	ID   uint64          `json:"id"`
	Type string          `json:"type"`
	Time time.Time       `json:"time"`
	Data json.RawMessage `json:"data"`
}

// ServiceStateEvent is the Data of EventServiceState.
type ServiceStateEvent struct {
	Service Service      `json:"service"`
	From    ServiceState `json:"from"`
}

// ServiceLogEvent is the Data of EventServiceLog.
type ServiceLogEvent struct {
	Service string  `json:"service"`
	Line    LogLine `json:"line"`
}

// JobProgressEvent is the Data of EventJobProgress.
type JobProgressEvent struct {
	JobID    string  `json:"jobId"`
	Progress float64 `json:"progress"`
	Message  string  `json:"message,omitempty"`
}

// JobDoneEvent is the Data of EventJobDone.
type JobDoneEvent struct {
	Job Job `json:"job"`
}

// WarningEvent is the Data of EventDaemonWarning.
type WarningEvent struct {
	Warning Warning `json:"warning"`
}

// NewEvent marshals data into an Event (ID and Time are set by the bus).
func NewEvent(typ string, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	return Event{Type: typ, Data: raw}, nil
}
