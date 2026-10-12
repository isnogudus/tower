package main

import (
	"context"
	"os"
	"syscall"
	"time"
)

// fileState is what a poll sees of a watched file. Comparing device and
// inode catches a file that was replaced by rename(2), which is how ACME
// clients and copy tools usually install a certificate; size and mtime
// catch a file rewritten in place.
type fileState struct {
	exists bool
	dev    uint64
	ino    uint64
	size   int64
	mtime  time.Time
}

// statFile returns the state of path, following symbolic links, so that a
// link switched to a new target counts as a change.
func statFile(path string) fileState {
	fi, err := os.Stat(path)
	if err != nil {
		return fileState{}
	}
	s := fileState{exists: true, size: fi.Size(), mtime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.dev = uint64(st.Dev)
		s.ino = uint64(st.Ino)
	}
	return s
}

// watcher decides, poll by poll, when the watched files have changed and
// settled. Files that are replaced one after another (a certificate, then
// its key) must not trigger a reload in between, or the service would load
// a certificate with the wrong key. So a change only fires once a poll
// finds no further change and every file exists. It keeps no clock of its
// own: the poll interval is the quiet period.
type watcher struct {
	paths   []string
	last    []fileState
	pending map[string]bool // files changed since the last firing
}

// newWatcher records the current state of paths as the baseline.
func newWatcher(paths []string, stat func(string) fileState) *watcher {
	w := &watcher{paths: paths, pending: map[string]bool{}}
	for _, p := range paths {
		w.last = append(w.last, stat(p))
	}
	return w
}

// poll looks at the files once. It returns the files that changed, in the
// order they were given, when the change is complete, and nil otherwise.
func (w *watcher) poll(stat func(string) fileState) []string {
	changedNow := false
	allExist := true
	for i, p := range w.paths {
		s := stat(p)
		if s != w.last[i] {
			w.last[i] = s
			w.pending[p] = true
			changedNow = true
		}
		if !s.exists {
			allExist = false
		}
	}
	if changedNow || !allExist || len(w.pending) == 0 {
		return nil
	}
	var changed []string
	for _, p := range w.paths {
		if w.pending[p] {
			changed = append(changed, p)
		}
	}
	w.pending = map[string]bool{}
	return changed
}

// watchFiles polls paths every interval until ctx ends and reports each
// completed change on the returned channel. A change that is not picked up
// yet is replaced by the next one, so the supervisor never acts on a stale
// list twice.
func watchFiles(ctx context.Context, paths []string, interval time.Duration) <-chan []string {
	out := make(chan []string, 1)
	w := newWatcher(paths, statFile)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			changed := w.poll(statFile)
			if changed == nil {
				continue
			}
			// Drop a change still waiting, then deliver the new one.
			select {
			case old := <-out:
				changed = mergePaths(paths, old, changed)
			default:
			}
			out <- changed
		}
	}()
	return out
}

// mergePaths returns the paths in a or b, in the order of all.
func mergePaths(all, a, b []string) []string {
	in := map[string]bool{}
	for _, p := range a {
		in[p] = true
	}
	for _, p := range b {
		in[p] = true
	}
	var out []string
	for _, p := range all {
		if in[p] {
			out = append(out, p)
		}
	}
	return out
}
