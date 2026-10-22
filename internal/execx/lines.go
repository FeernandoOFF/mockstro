package execx

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

// Lines serializes stdout/stderr, tees their bytes, and emits complete display lines.
// Flush emits the final unterminated line after the command exits.
type Lines struct {
	mu   sync.Mutex
	buf  []byte
	tee  io.Writer
	emit func(string)
}

func NewLines(tee io.Writer, emit func(string)) *Lines {
	return &Lines{tee: tee, emit: emit}
}

func (w *Lines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tee != nil {
		if _, err := w.tee.Write(p); err != nil {
			return 0, err
		}
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.line(strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func (w *Lines) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.line(string(w.buf))
		w.buf = nil
	}
}

func (w *Lines) line(s string) {
	if i := strings.LastIndex(s, "\r"); i >= 0 {
		s = s[i+1:]
	}
	if w.emit != nil {
		w.emit(s)
	}
}
