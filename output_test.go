package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func collect() (*lineWriter, *[]string) {
	var lines []string
	return newLineWriter(func(l string) { lines = append(lines, l) }), &lines
}

func TestLineWriterSplitsLines(t *testing.T) {
	w, lines := collect()
	w.Write([]byte("one\ntw"))
	w.Write([]byte("o\r\n\n\nthree"))

	want := []string{"one", "two"}
	if !reflect.DeepEqual(*lines, want) {
		t.Fatalf("before Flush: got %q, want %q", *lines, want)
	}

	w.Flush()
	w.Flush()
	want = append(want, "three")
	if !reflect.DeepEqual(*lines, want) {
		t.Fatalf("after Flush: got %q, want %q", *lines, want)
	}
}

func TestLineWriterSplitsLongLines(t *testing.T) {
	w, lines := collect()
	w.Write([]byte(strings.Repeat("x", 2*maxLogLine+10)))
	w.Write([]byte("\n"))

	if len(*lines) != 3 {
		t.Fatalf("got %d messages, want 3", len(*lines))
	}
	for i, n := range []int{maxLogLine, maxLogLine, 10} {
		if len((*lines)[i]) != n {
			t.Errorf("message %d: %d bytes, want %d", i, len((*lines)[i]), n)
		}
	}
}

func TestChildOutputReachesLineWriter(t *testing.T) {
	w, lines := collect()
	code := Run(context.Background(), Options{
		Cmd:    []string{"sh", "-c", "echo out; echo err >&2; printf unfinished"},
		Grace:  time.Second,
		Policy: fastPolicy(),
		Log:    &bytes.Buffer{},
		Stdout: w,
		Stderr: w,
	})
	if code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	want := []string{"out", "err", "unfinished"}
	if !reflect.DeepEqual(*lines, want) {
		t.Fatalf("got %q, want %q", *lines, want)
	}
}

// A grandchild that keeps the output pipe open must not keep tower from
// noticing that the child is gone.
func TestLingeringGrandchildDoesNotBlockExit(t *testing.T) {
	w, _ := collect()
	start := time.Now()
	code := Run(context.Background(), Options{
		Cmd:    []string{"sh", "-c", "sleep 30 & exit 0"},
		Grace:  200 * time.Millisecond,
		Policy: fastPolicy(),
		Log:    &bytes.Buffer{},
		Stdout: w,
		Stderr: w,
	})
	if code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Run took %s", d)
	}
}
