package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func writeEnvDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestEnvFromDir(t *testing.T) {
	dir := writeEnvDir(t, map[string]string{
		"PLAIN":    "value\n",
		"NOEOL":    "value",
		"FIRST":    "one\ntwo\n",
		"TRAILING": "value \t \n",
		"LEADING":  "  value\n",
		"NUL":      "a\x00b\n",
		"BLANK":    "\n",
		"REMOVED":  "",
		"REPLACED": "new\n",
		".hidden":  "x\n",
	})
	if err := os.Mkdir(filepath.Join(dir, "SUBDIR"), 0o755); err != nil {
		t.Fatal(err)
	}

	base := []string{"KEEP=1", "REMOVED=old", "REPLACED=old"}
	env, err := envFromDir(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(env)
	want := []string{
		"BLANK=",
		"FIRST=one",
		"KEEP=1",
		"LEADING=  value",
		"NOEOL=value",
		"NUL=a\nb",
		"PLAIN=value",
		"REPLACED=new",
		"TRAILING=value",
	}
	if strings.Join(env, "|") != strings.Join(want, "|") {
		t.Errorf("environment wrong:\n got %q\nwant %q", env, want)
	}
}

func TestEnvFromDirErrors(t *testing.T) {
	if _, err := envFromDir(nil, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Errorf("missing directory: expected error")
	}
	if _, err := envFromDir(nil, writeEnvDir(t, map[string]string{"A=B": "x"})); err == nil {
		t.Errorf("name with '=': expected error")
	}
}

func runWithEnvDir(t *testing.T, p Policy, envDir, script string) (int, string, string) {
	t.Helper()
	var logbuf, out bytes.Buffer
	code := Run(context.Background(), Options{
		Cmd:    []string{"sh", "-c", script},
		EnvDir: envDir,
		Grace:  time.Second,
		Policy: p,
		Log:    &logbuf,
		Stdout: &out,
		Stderr: &bytes.Buffer{},
	})
	return code, out.String(), logbuf.String()
}

func TestEnvDirReachesChild(t *testing.T) {
	t.Setenv("TOWER_TEST_GONE", "still here")
	dir := writeEnvDir(t, map[string]string{"TOWER_TEST_SET": "hello\n", "TOWER_TEST_GONE": ""})
	code, out, logs := runWithEnvDir(t, fastPolicy(), dir,
		`echo "$TOWER_TEST_SET/${TOWER_TEST_GONE-unset}"`)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d, log: %s", code, logs)
	}
	if strings.TrimSpace(out) != "hello/unset" {
		t.Errorf("child saw %q", out)
	}
}

// The directory is read on every start: the child changes its own variable
// and sees the new value after the restart.
func TestEnvDirIsReadOnEveryStart(t *testing.T) {
	dir := writeEnvDir(t, map[string]string{"TOWER_TEST_RUN": "first\n"})
	file := filepath.Join(dir, "TOWER_TEST_RUN")
	code, out, logs := runWithEnvDir(t, fastPolicy(), dir,
		`echo "$TOWER_TEST_RUN"; [ "$TOWER_TEST_RUN" = second ] && exit 0; echo second > `+file+`; exit 3`)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d, log: %s", code, logs)
	}
	if strings.Join(strings.Fields(out), " ") != "first second" {
		t.Errorf("child saw %q", out)
	}
}

func TestEnvDirMissingIsStartFailure(t *testing.T) {
	code, _, logs := runWithEnvDir(t, fastPolicy(), filepath.Join(t.TempDir(), "missing"), "exit 0")
	if code != 127 {
		t.Errorf("expected 127, got %d", code)
	}
	if !strings.Contains(logs, "environment directory") {
		t.Errorf("log should name the environment directory: %s", logs)
	}
}
