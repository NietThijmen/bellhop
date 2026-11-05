package sites

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/nietthijmen/bellhop/internal/platform"
)

// Watch watches the top level of every parked root until ctx is cancelled.
// A burst of changes in a root is announced once, after Debounce of quiet
// time, as a sites.changed event with reason "filesystem" and through
// OnChange. Roots that cannot be watched (missing, unreadable, or not
// mounted yet) are retried every Resync. Park and Forget update the set
// immediately. Watch returns nil when ctx is cancelled.
func (m *Manager) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watch parked folders: %w; new folders still show up in the site list, but restart the daemon if the proxy does not reload on its own", err)
	}
	defer func() { _ = w.Close() }()

	// The timer stays stopped until a change arrives. Reset is only called
	// from this goroutine, which is also the only receiver of timer.C.
	timer := time.NewTimer(m.o.Debounce)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	var (
		timerC  <-chan time.Time
		pending ChangeEvent
		armed   bool
	)
	arm := func(path string) {
		if path == "" {
			return
		}
		if !armed {
			pending = ChangeEvent{Reason: ReasonFS, Path: path}
			armed = true
		} else if pending.Path != path {
			pending.Path = "" // more than one root changed in this window
		}
		timer.Reset(m.o.Debounce)
		timerC = timer.C
	}

	failed := map[string]struct{}{}
	wanted := map[string]struct{}{}
	m.syncWatches(ctx, w, failed, wanted, arm)

	resync := time.NewTicker(m.o.Resync)
	defer resync.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.rootsChanged:
			m.syncWatches(ctx, w, failed, wanted, arm)
		case <-resync.C:
			m.syncWatches(ctx, w, failed, wanted, arm)
		case <-timerC:
			timerC = nil
			if !armed {
				continue
			}
			ev := pending
			armed = false
			m.changed(ev)
		case ev, ok := <-w.Events:
			if !ok {
				return errors.New("parked folder watch closed; restart the daemon to watch parked folders again")
			}
			if !relevantWatchOp(ev.Op) {
				continue
			}
			arm(matchWatched(wanted, ev.Name))
		case werr, ok := <-w.Errors:
			if !ok {
				return errors.New("parked folder watch closed; restart the daemon to watch parked folders again")
			}
			if werr != nil && ctx.Err() == nil {
				slog.Warn("parked folder watch", "err", werr)
			}
		}
	}
}

// syncWatches makes the watcher match the parked roots in the store.
// A root that was previously unwatchable and now accepts a watch is announced
// through arm so sites on a volume that just mounted are served.
func (m *Manager) syncWatches(ctx context.Context, w *fsnotify.Watcher, failed, wanted map[string]struct{}, arm func(string)) {
	if ctx.Err() != nil {
		return
	}
	roots, err := m.o.Store.ParkedPaths(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("watch parked folders: cannot list them", "err", err)
		}
		return
	}
	for path := range wanted {
		delete(wanted, path)
	}
	for _, root := range roots {
		wanted[root] = struct{}{}
	}
	for path := range failed {
		if !samePathIn(roots, path) {
			delete(failed, path)
		}
	}

	for _, have := range w.WatchList() {
		if !samePathIn(roots, have) {
			removeWatch(w, have)
			continue
		}
		if _, err := os.Stat(have); err != nil {
			// The watch is stale (the folder was removed, or the volume went
			// away). Drop it so the add below records the failure and a later
			// resync can pick the folder up again.
			removeWatch(w, have)
		}
	}

	watching := w.WatchList()
	for _, root := range roots {
		if samePathIn(watching, root) {
			delete(failed, root)
			continue
		}
		if err := w.Add(root); err != nil {
			if _, seen := failed[root]; !seen {
				slog.Warn("cannot watch parked folder", "path", root, "err", err)
				failed[root] = struct{}{}
			}
			continue
		}
		if _, was := failed[root]; was {
			delete(failed, root)
			slog.Info("parked folder is watchable again", "path", root)
			arm(root)
		}
	}
}

func removeWatch(w *fsnotify.Watcher, path string) {
	if err := w.Remove(path); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
		slog.Debug("stop watching parked folder", "path", path, "err", err)
	}
}

// relevantWatchOp reports whether op can change the top-level site list.
// Write is included because the macOS backend reports new directory entries
// as Write on the parent rather than Create.
func relevantWatchOp(op fsnotify.Op) bool {
	return op.Has(fsnotify.Create) || op.Has(fsnotify.Write) || op.Has(fsnotify.Remove) || op.Has(fsnotify.Rename)
}

// matchWatched returns the parked root evName belongs to: the path itself
// when the root was removed, or its parent when a child was created.
func matchWatched(wanted map[string]struct{}, evName string) string {
	if evName == "" {
		return ""
	}
	if _, ok := wanted[evName]; ok {
		return evName
	}
	dir := filepath.Dir(evName)
	for root := range wanted {
		if platform.SamePath(root, evName) || platform.SamePath(root, dir) {
			return root
		}
	}
	return dir
}

func samePathIn(paths []string, path string) bool {
	for _, other := range paths {
		if platform.SamePath(other, path) {
			return true
		}
	}
	return false
}
