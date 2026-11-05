package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
)

// Log stream names used in api.LogLine.Stream.
const (
	StreamStdout     = "stdout"
	StreamStderr     = "stderr"
	StreamSupervisor = "supervisor"
)

// maxLineBytes caps a single log line; longer output is split.
const maxLineBytes = 64 * 1024

// ring is a fixed-capacity circular buffer of log lines.
type ring struct {
	buf   []api.LogLine
	start int // index of oldest element
	n     int
}

func newRing(size int) *ring { return &ring{buf: make([]api.LogLine, size)} }

func (r *ring) add(l api.LogLine) {
	if len(r.buf) == 0 {
		return
	}
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = l
		r.n++
		return
	}
	r.buf[r.start] = l
	r.start = (r.start + 1) % len(r.buf)
}

// tail returns up to n most recent lines, oldest first. n <= 0 means all.
func (r *ring) tail(n int) []api.LogLine {
	if n <= 0 || n > r.n {
		n = r.n
	}
	out := make([]api.LogLine, n)
	for i := range n {
		out[i] = r.buf[(r.start+r.n-n+i)%len(r.buf)]
	}
	return out
}

// RotatingFile is an io.WriteCloser that appends to path and rotates it when
// it would grow beyond maxBytes, keeping at most files files in total:
// path, path.1, ..., path.<files-1> (path.1 is the most recent backup).
// The file is opened lazily on the first write. It is safe for concurrent use.
type RotatingFile struct {
	path     string
	maxBytes int64
	files    int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// NewRotatingFile returns a rotating writer for path. maxBytes <= 0 disables
// rotation; files < 1 is treated as 1 (truncate instead of keeping backups).
func NewRotatingFile(path string, maxBytes int64, files int) *RotatingFile {
	if files < 1 {
		files = 1
	}
	return &RotatingFile{path: path, maxBytes: maxBytes, files: files}
}

// Write appends p, rotating first if p would push the file past maxBytes.
func (w *RotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := w.openLocked(); err != nil {
			return 0, err
		}
	}
	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingFile) openLocked() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *RotatingFile) rotateLocked() error {
	// Close first: Windows cannot rename an open file.
	if err := w.f.Close(); err != nil {
		slog.Warn("supervisor: close log file before rotation", "path", w.path, "err", err)
	}
	w.f = nil
	if w.files == 1 {
		if err := os.Remove(w.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate log file: %w", err)
		}
	} else {
		_ = os.Remove(w.backup(w.files - 1))
		for i := w.files - 2; i >= 1; i-- {
			if err := os.Rename(w.backup(i), w.backup(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("rotate log file: %w", err)
			}
		}
		if err := os.Rename(w.path, w.backup(1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate log file: %w", err)
		}
	}
	return w.openLocked()
}

func (w *RotatingFile) backup(i int) string { return w.path + "." + strconv.Itoa(i) }

// Close closes the current file; a later Write reopens it.
func (w *RotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// sink receives every log line of one service: it keeps the ring buffer,
// appends to the rotating file and publishes a service.log event.
type sink struct {
	service string
	pub     events.Publisher
	now     func() time.Time

	mu      sync.Mutex
	ring    *ring
	file    *RotatingFile // nil when file logging is disabled
	fileErr bool          // a file error has been logged already
}

func (k *sink) add(stream, line string) {
	l := api.LogLine{Time: k.now().UTC(), Stream: stream, Line: line}
	k.mu.Lock()
	k.ring.add(l)
	if k.file != nil {
		rec := l.Time.Format("2006-01-02T15:04:05.000Z07:00") + " " + stream + " " + line + "\n"
		if _, err := io.WriteString(k.file, rec); err != nil && !k.fileErr {
			k.fileErr = true
			slog.Error("supervisor: cannot write service log file; output is still kept in memory",
				"service", k.service, "path", k.file.path, "err", err)
		} else if err == nil {
			k.fileErr = false
		}
	}
	k.mu.Unlock()
	k.pub.Publish(api.EventServiceLog, api.ServiceLogEvent{Service: k.service, Line: l})
}

func (k *sink) tail(n int) []api.LogLine {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.ring.tail(n)
}

func (k *sink) closeFile() {
	if k.file == nil {
		return
	}
	if err := k.file.Close(); err != nil {
		slog.Warn("supervisor: close service log file", "service", k.service, "err", err)
	}
}

// pump reads r line by line into the sink until EOF or error. Lines longer
// than maxLineBytes are split; a trailing "\r" is removed.
func (k *sink) pump(r io.Reader, stream string) {
	br := bufio.NewReaderSize(r, 4096)
	var cur []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 {
			cur = append(cur, chunk...)
		}
		if err != nil {
			if len(cur) > 0 {
				k.add(stream, strings.TrimRight(string(cur), "\r"))
			}
			return
		}
		for len(cur) >= maxLineBytes && (isPrefix || len(cur) > maxLineBytes) {
			k.add(stream, string(cur[:maxLineBytes]))
			cur = append(cur[:0], cur[maxLineBytes:]...)
		}
		if isPrefix {
			continue
		}
		k.add(stream, strings.TrimRight(string(cur), "\r"))
		cur = cur[:0]
	}
}
