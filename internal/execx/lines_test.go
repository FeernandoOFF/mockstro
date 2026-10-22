package execx_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
)

func TestLinesTeeChunksProgressAndFlush(t *testing.T) {
	var tee bytes.Buffer
	var lines []string
	w := execx.NewLines(&tee, func(s string) { lines = append(lines, s) })
	chunks := []string{"first\r", "\nold\rnew\npar", "tial"}
	for _, chunk := range chunks {
		if n, err := io.WriteString(w, chunk); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if !reflect.DeepEqual(lines, []string{"first", "new"}) {
		t.Fatalf("before flush: %q", lines)
	}
	w.Flush()
	w.Flush()
	if !reflect.DeepEqual(lines, []string{"first", "new", "partial"}) || tee.String() != "first\r\nold\rnew\npartial" {
		t.Fatalf("lines %q tee %q", lines, tee.String())
	}
}

func TestLinesConcurrentStreams(t *testing.T) {
	var tee bytes.Buffer
	var lines []string
	w := execx.NewLines(&tee, func(s string) { lines = append(lines, s) })
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) { defer wg.Done(); fmt.Fprintf(w, "line %d\n", i) }(i)
	}
	wg.Wait()
	w.Flush()
	seen := map[string]bool{}
	for _, line := range lines {
		seen[line] = true
	}
	if len(lines) != 20 || len(seen) != 20 || bytes.Count(tee.Bytes(), []byte("\n")) != 20 {
		t.Fatalf("lines %v tee %q", lines, tee.String())
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
func TestLinesPropagatesTeeFailure(t *testing.T) {
	boom := errors.New("disk full")
	w := execx.NewLines(failingWriter{boom}, func(string) { t.Error("emitted unwritten output") })
	if n, err := io.WriteString(w, "hello\n"); n != 0 || !errors.Is(err, boom) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	w.Flush()
}
