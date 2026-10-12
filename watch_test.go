package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// fakeFiles is a file system for the watcher: the state of each path.
type fakeFiles map[string]fileState

func (f fakeFiles) stat(path string) fileState { return f[path] }

func present(ino uint64, size int64) fileState {
	return fileState{exists: true, ino: ino, size: size}
}

func TestWatcherFiresOnceTheChangeSettled(t *testing.T) {
	fs := fakeFiles{"cert": present(1, 100), "key": present(2, 50)}
	w := newWatcher([]string{"cert", "key"}, fs.stat)

	if got := w.poll(fs.stat); got != nil {
		t.Fatalf("no change, but fired: %v", got)
	}

	// The certificate is replaced (new inode); the key follows only later.
	fs["cert"] = present(3, 120)
	if got := w.poll(fs.stat); got != nil {
		t.Fatalf("fired while files were still changing: %v", got)
	}
	fs["key"] = present(4, 51)
	if got := w.poll(fs.stat); got != nil {
		t.Fatalf("fired while files were still changing: %v", got)
	}

	// A poll without further change completes it, with both files.
	if got := w.poll(fs.stat); !slices.Equal(got, []string{"cert", "key"}) {
		t.Fatalf("got %v, want [cert key]", got)
	}
	if got := w.poll(fs.stat); got != nil {
		t.Fatalf("fired twice for one change: %v", got)
	}
}

func TestWatcherWaitsForMissingFiles(t *testing.T) {
	fs := fakeFiles{"cert": present(1, 100)}
	w := newWatcher([]string{"cert"}, fs.stat)

	// Removed, then installed again: no reload of a missing file.
	fs["cert"] = fileState{}
	if got := w.poll(fs.stat); got != nil {
		t.Fatalf("fired on removal: %v", got)
	}
	if got := w.poll(fs.stat); got != nil {
		t.Fatalf("fired while the file is missing: %v", got)
	}
	fs["cert"] = present(2, 100)
	if got := w.poll(fs.stat); got != nil {
		t.Fatalf("fired before the change settled: %v", got)
	}
	if got := w.poll(fs.stat); !slices.Equal(got, []string{"cert"}) {
		t.Fatalf("got %v, want [cert]", got)
	}
}

func TestWatcherSeesRewriteInPlace(t *testing.T) {
	fs := fakeFiles{"cert": present(1, 100)}
	w := newWatcher([]string{"cert"}, fs.stat)

	s := fs["cert"]
	s.mtime = time.Unix(1700000000, 0)
	fs["cert"] = s
	w.poll(fs.stat)
	if got := w.poll(fs.stat); !slices.Equal(got, []string{"cert"}) {
		t.Fatalf("got %v, want [cert]", got)
	}
}

func TestStatFileSeesRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := statFile(path)

	// Same size, and the mtime may even match within the clock's
	// resolution: only the inode tells the new file apart.
	tmp := filepath.Join(dir, "cert.pem.new")
	if err := os.WriteFile(tmp, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, before.mtime, before.mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	if after := statFile(path); after == before {
		t.Errorf("replaced file looks unchanged: %+v", after)
	}
	if missing := statFile(filepath.Join(dir, "none")); missing.exists {
		t.Errorf("missing file reported as existing")
	}
}

func TestWatchFilesReportsChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := watchFiles(ctx, []string{path}, 10*time.Millisecond)

	replace(t, path, "new certificate")
	select {
	case got := <-changes:
		if !slices.Equal(got, []string{path}) {
			t.Errorf("got %v, want [%s]", got, path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported")
	}
}

func TestMergePaths(t *testing.T) {
	all := []string{"a", "b", "c"}
	if got := mergePaths(all, []string{"c"}, []string{"a", "c"}); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("got %v, want [a c]", got)
	}
}

// replace installs a new version of path the way ACME clients do: write a
// temporary file, then rename it over the old one.
func replace(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}
