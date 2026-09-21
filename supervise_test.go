package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fastPolicy() Policy {
	return Policy{
		NoRestart: map[int]bool{0: true, 1: true},
		Initial:   10 * time.Millisecond,
		Max:       40 * time.Millisecond,
		Stable:    time.Hour,
		Window:    time.Hour,
	}
}

// counterScript returns an sh script that increments a counter in a file
// on every run and exits with failCode until the counter reaches failures;
// after that it exits with finalCode.
func counterScript(t *testing.T, failures, failCode, finalCode int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	if err := os.WriteFile(counter, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "n=$(cat " + counter + "); n=$((n+1)); echo $n > " + counter +
		"; if [ $n -le " + itoa(failures) + " ]; then exit " + itoa(failCode) +
		"; fi; exit " + itoa(finalCode)
	return script, counter
}

func readCounter(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return atoi(strings.TrimSpace(string(b)))
}

func runWith(t *testing.T, ctx context.Context, p Policy, grace time.Duration, script string) (int, string) {
	t.Helper()
	var logbuf bytes.Buffer
	code := Run(ctx, Options{
		Cmd:    []string{"sh", "-c", script},
		Grace:  grace,
		Policy: p,
		Log:    &logbuf,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	return code, logbuf.String()
}

func TestCleanExitEndsWithZero(t *testing.T) {
	code, logs := runWith(t, context.Background(), fastPolicy(), time.Second, "exit 0")
	if code != 0 {
		t.Errorf("expected exit 0, got %d", code)
	}
	if strings.Contains(logs, "restart") {
		t.Errorf("no restart expected, log: %s", logs)
	}
}

func TestConfigErrorIsNotRestarted(t *testing.T) {
	script, counter := counterScript(t, 10, 1, 0)
	code, _ := runWith(t, context.Background(), fastPolicy(), time.Second, script)
	if code != 1 {
		t.Errorf("expected exit 1 (config error passed through), got %d", code)
	}
	if n := readCounter(t, counter); n != 1 {
		t.Errorf("expected exactly one start, got %d", n)
	}
}

func TestChildIsRestartedUntilCleanExit(t *testing.T) {
	script, counter := counterScript(t, 2, 4, 0)
	code, logs := runWith(t, context.Background(), fastPolicy(), time.Second, script)
	if code != 0 {
		t.Errorf("expected exit 0, got %d", code)
	}
	if n := readCounter(t, counter); n != 3 {
		t.Errorf("expected 3 starts (2 failures + 1 clean), got %d", n)
	}
	if got := strings.Count(logs, "restart"); got != 2 {
		t.Errorf("expected 2 restart lines in the log, got %d:\n%s", got, logs)
	}
	if !strings.Contains(logs, "exit=4") {
		t.Errorf("log should name the exit code:\n%s", logs)
	}
}

func TestGiveUpReturnsLastExitCode(t *testing.T) {
	p := fastPolicy()
	p.GiveUp = 2
	script, counter := counterScript(t, 100, 5, 0)
	code, logs := runWith(t, context.Background(), p, time.Second, script)
	if code != 5 {
		t.Errorf("expected exit 5 (child's last exit code), got %d", code)
	}
	if n := readCounter(t, counter); n != 3 {
		t.Errorf("expected 3 starts (1 + 2 restarts), got %d", n)
	}
	if !strings.Contains(logs, "giving up") {
		t.Errorf("log should mention giving up:\n%s", logs)
	}
}

func TestSignalKilledChildIsRestarted(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	os.WriteFile(counter, []byte("0"), 0o644)
	script := "n=$(cat " + counter + "); n=$((n+1)); echo $n > " + counter +
		"; if [ $n -le 1 ]; then kill -9 $$; fi; exit 0"
	code, logs := runWith(t, context.Background(), fastPolicy(), time.Second, script)
	if code != 0 {
		t.Errorf("expected exit 0, got %d", code)
	}
	if n := readCounter(t, counter); n != 2 {
		t.Errorf("expected 2 starts, got %d", n)
	}
	if !strings.Contains(logs, "signal=killed") {
		t.Errorf("log should name the signal:\n%s", logs)
	}
}

func TestCancelTerminatesChildAndReturnsZero(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	code, _ := runWith(t, ctx, fastPolicy(), 2*time.Second, "sleep 30")
	if code != 0 {
		t.Errorf("expected exit 0 after stop, got %d", code)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("stop should take effect immediately, took %v", d)
	}
}

func TestCancelDuringBackoffDoesNotRestart(t *testing.T) {
	p := fastPolicy()
	p.Initial = 2 * time.Second
	p.Max = 2 * time.Second
	script, counter := counterScript(t, 10, 4, 0)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond) // child is dead, backoff running
		cancel()
	}()
	start := time.Now()
	code, _ := runWith(t, ctx, p, time.Second, script)
	if code != 0 {
		t.Errorf("expected exit 0 after stop, got %d", code)
	}
	if n := readCounter(t, counter); n != 1 {
		t.Errorf("stop during backoff must not restart, starts: %d", n)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("stop during backoff should take effect immediately, took %v", d)
	}
}

func TestGraceThenKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	grace := 300 * time.Millisecond
	start := time.Now()
	// The child ignores SIGTERM; the loop survives the death of single
	// sleep processes.
	code, logs := runWith(t, ctx, fastPolicy(), grace, "trap '' TERM; while :; do sleep 0.05; done")
	if code != 0 {
		t.Errorf("expected exit 0 after stop, got %d", code)
	}
	d := time.Since(start)
	if d < 100*time.Millisecond+grace {
		t.Errorf("SIGKILL came before the grace period ended: %v", d)
	}
	if d > 100*time.Millisecond+grace+time.Second {
		t.Errorf("SIGKILL came too late: %v", d)
	}
	if !strings.Contains(logs, "kill") {
		t.Errorf("log should mention the kill:\n%s", logs)
	}
}

func TestChildRunsInDir(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	code := Run(context.Background(), Options{
		Cmd:    []string{"sh", "-c", "pwd"},
		Dir:    dir,
		Grace:  time.Second,
		Policy: fastPolicy(),
		Log:    &bytes.Buffer{},
		Stdout: &out,
		Stderr: &bytes.Buffer{},
	})
	if code != 0 {
		t.Fatalf("Exit %d", code)
	}
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(out.String()))
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Errorf("working directory: expected %s, got %s", want, got)
	}
}

func TestMissingCommandReturnsErrorCode(t *testing.T) {
	var logbuf bytes.Buffer
	code := Run(context.Background(), Options{
		Cmd:    []string{"/nonexistent/binary"},
		Grace:  time.Second,
		Policy: fastPolicy(),
		Log:    &logbuf,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if code == 0 {
		t.Errorf("a missing binary must end with an error")
	}
	if !strings.Contains(logbuf.String(), "nonexistent") {
		t.Errorf("log should name the reason:\n%s", logbuf.String())
	}
}
