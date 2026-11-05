package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/nietthijmen/bellhop/internal/api"
)

// maxSSELine bounds a single SSE line read by the client.
const maxSSELine = 4 << 20

// StartSSE prepares w for an event stream: it sets the SSE headers, writes
// the 200 status and flushes so the client sees the response immediately.
// Daemon handlers should subscribe to the bus before calling it, so a client
// that waits for the response headers knows it will not miss later events.
func StartSSE(w http.ResponseWriter) error {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := http.NewResponseController(w).Flush(); err != nil {
		return fmt.Errorf("start event stream: %w", err)
	}
	return nil
}

// WriteSSE writes ev as one SSE frame:
// "id: N\nevent: T\ndata: <json(ev)>\n\n". If w can be flushed it is.
func WriteSSE(w io.Writer, ev api.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode event %d: %w", ev.ID, err)
	}
	var buf bytes.Buffer
	buf.Grow(len(data) + len(ev.Type) + 32)
	buf.WriteString("id: ")
	buf.WriteString(strconv.FormatUint(ev.ID, 10))
	buf.WriteString("\nevent: ")
	buf.WriteString(strings.ReplaceAll(ev.Type, "\n", ""))
	buf.WriteString("\ndata: ")
	buf.Write(data) // json.Marshal never emits raw newlines
	buf.WriteString("\n\n")
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write event %d: %w", ev.ID, err)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// WriteJSON writes v as a JSON response with the given status. A nil v
// writes no body. Encoding errors are logged; the status is already sent.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	if v == nil {
		w.WriteHeader(status)
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("ipc: cannot encode response", "err", err)
		WriteError(w, http.StatusInternalServerError, api.Error{
			Code:    api.CodeInternal,
			Message: "the daemon could not encode its response",
			Hint:    "check the daemon log",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		slog.Debug("ipc: write response", "err", err)
	}
}

// WriteError writes e as {"error": {...}} with the given status.
func WriteError(w http.ResponseWriter, status int, e api.Error) {
	if e.Code == "" {
		e.Code = api.CodeInternal
	}
	WriteJSON(w, status, api.ErrorResponse{Error: e})
}

// sseReader parses an SSE stream into api.Events.
type sseReader struct {
	sc *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	return &sseReader{sc: sc}
}

// next returns the next event. It returns io.EOF when the stream ends at a
// frame boundary.
func (s *sseReader) next() (api.Event, error) {
	var (
		id, typ string
		data    []string
	)
	for s.sc.Scan() {
		line := s.sc.Text()
		if line == "" {
			if len(data) == 0 {
				id, typ = "", "" // nothing to dispatch
				continue
			}
			return decodeSSE(id, typ, strings.Join(data, "\n"))
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / heartbeat
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "event":
			typ = value
		case "data":
			data = append(data, value)
		}
	}
	if err := s.sc.Err(); err != nil {
		return api.Event{}, err
	}
	return api.Event{}, io.EOF
}

func decodeSSE(id, typ, data string) (api.Event, error) {
	var ev api.Event
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return api.Event{}, fmt.Errorf("decode event %q: %w", id, err)
	}
	if ev.ID == 0 && id != "" {
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			return api.Event{}, errors.New("event has invalid id " + strconv.Quote(id))
		}
		ev.ID = n
	}
	if ev.Type == "" {
		ev.Type = typ
	}
	return ev, nil
}
