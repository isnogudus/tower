package main

import (
	"bytes"
	"sync"
)

// maxLogLine bounds one message; longer lines are split. syslogd cuts
// messages off at a few KiB anyway.
const maxLogLine = 4096

// lineWriter turns the byte stream of the child into messages: one call
// of emit per line, without the line ending. Empty lines are dropped. An
// unfinished line stays in the buffer until the rest arrives or Flush is
// called, so a child that dies in the middle of a line loses nothing.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func(line string)
}

func newLineWriter(emit func(line string)) *lineWriter {
	return &lineWriter{emit: emit}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.line(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	for len(w.buf) >= maxLogLine {
		w.line(w.buf[:maxLogLine])
		w.buf = w.buf[maxLogLine:]
	}
	// Do not hold on to the backing array of a large write.
	w.buf = append([]byte(nil), w.buf...)
	return len(p), nil
}

// Flush emits an unfinished last line.
func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.line(w.buf)
	w.buf = nil
}

func (w *lineWriter) line(b []byte) {
	for len(b) > maxLogLine {
		w.emit(string(b[:maxLogLine]))
		b = b[maxLogLine:]
	}
	b = bytes.TrimRight(b, "\r")
	if len(b) > 0 {
		w.emit(string(b))
	}
}
