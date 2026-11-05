package cli

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/ipc"
)

// waitJob waits for a daemon job started by a command, rendering progress
// to the command's stdout (suppressed with --json, which prints the final
// job instead). A failed or cancelled job returns an *ExitError with
// ExitJobFailed. Job-based commands (php install, db install, ...) call it
// with the JobRef the daemon returned.
func (a *app) waitJob(cmd *cobra.Command, c *ipc.Client, ref api.JobRef) (api.Job, error) {
	if ref.JobID == "" {
		return api.Job{}, errors.New("the daemon did not return a job id (the client and daemon versions may differ)")
	}
	out := cmd.OutOrStdout()
	var p *progressLine
	var onProgress func(api.JobProgressEvent)
	if !a.json {
		p = newProgressLine(out)
		onProgress = p.update
	}
	j, err := c.WaitJob(cmd.Context(), ref.JobID, onProgress)
	if p != nil {
		p.finish()
	}
	if err != nil {
		if cmd.Context().Err() != nil {
			return api.Job{}, &ExitError{Code: ExitUser, Err: err,
				Hint: fmt.Sprintf("Job %s keeps running in the daemon.", ref.JobID)}
		}
		return api.Job{}, err
	}
	if a.json {
		if werr := writeJSON(out, j, true); werr != nil {
			return j, werr
		}
	}
	return j, jobError(j)
}

// jobError maps a terminal job to nil or an *ExitError(ExitJobFailed).
func jobError(j api.Job) error {
	hint := ""
	if j.LogPath != "" {
		hint = "See the job log: " + j.LogPath
	}
	switch j.Status {
	case api.JobSucceeded:
		return nil
	case api.JobCancelled:
		msg := fmt.Sprintf("%s job %s was cancelled", j.Kind, j.ID)
		if j.Error != "" {
			msg += ": " + j.Error
		}
		return &ExitError{Code: ExitJobFailed, Err: errors.New(msg), Hint: hint}
	default:
		msg := fmt.Sprintf("%s job %s failed", j.Kind, j.ID)
		if j.Error != "" {
			msg += ": " + j.Error
		}
		return &ExitError{Code: ExitJobFailed, Err: errors.New(msg), Hint: hint}
	}
}

// progressLine renders job progress. On a terminal it rewrites one line in
// place; otherwise it prints a new line whenever the percentage or message
// changes, so logs and tests stay readable.
type progressLine struct {
	w       io.Writer
	inPlace bool
	last    string
	width   int
}

func newProgressLine(w io.Writer) *progressLine {
	return &progressLine{w: w, inPlace: isTerminal(w)}
}

func (p *progressLine) update(ev api.JobProgressEvent) {
	s := formatProgress(ev.Progress, ev.Message)
	if s == p.last {
		return
	}
	p.last = s
	if !p.inPlace {
		_, _ = fmt.Fprintln(p.w, s)
		return
	}
	pad := ""
	if n := p.width - len(s); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	p.width = len(s)
	_, _ = fmt.Fprint(p.w, "\r"+s+pad)
}

// finish ends an in-place line so later output starts on a fresh line.
func (p *progressLine) finish() {
	if p.inPlace && p.last != "" {
		_, _ = fmt.Fprintln(p.w)
	}
}

func formatProgress(f float64, msg string) string {
	pct := "[ ... ]"
	if f >= 0 && !math.IsNaN(f) {
		pct = fmt.Sprintf("[%4d%%]", int(math.Round(math.Min(f, 1)*100)))
	}
	if msg == "" {
		return pct
	}
	return pct + " " + oneLine(msg)
}

// isTerminal reports whether w is a character device (a console or tty).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
