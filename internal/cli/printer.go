package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/project"
)

// Printer renders engine events as plain text for terminals, agents and logs.
type Printer struct {
	Out, Err io.Writer
	Parallel bool
	Quiet    bool // hide raw tool output
	mu       sync.Mutex
}

func NewPrinter() *Printer { return &Printer{Out: os.Stdout, Err: os.Stderr} }

func (p *Printer) prefix(w int) string {
	if !p.Parallel || w == 0 {
		return ""
	}
	return "[w" + strconv.Itoa(w) + "] "
}

func (p *Printer) Handle(ev engine.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prefix := p.prefix(ev.Worker)
	if ev.Platform != "" {
		prefix = "[" + ev.Platform + "] " + prefix
	}
	switch ev.Kind {
	case engine.EvLog:
		w := p.Out
		if ev.Level != "info" {
			w = p.Err
		}
		for _, line := range strings.Split(ev.Text, "\n") {
			fmt.Fprintln(w, prefix+line)
		}
	case engine.EvOutput:
		if !p.Quiet {
			fmt.Fprintln(p.Out, prefix+ev.Text)
		}
	case engine.EvWorker:
		if p.Parallel {
			fmt.Fprintf(p.Out, "%sdevice %s, port %d, run dir %s\n", prefix, ev.Device, ev.Port, ev.Text)
		}
	case engine.EvFlowResult:
		r := ev.Result
		status := "PASS"
		if !r.Passed() {
			status = "FAIL (" + r.Stage + ")"
		}
		fmt.Fprintf(p.Out, "%s%s %s/%s in %ds\n", prefix, status, r.Feature, r.File, int(r.Duration.Seconds()))
		if r.Message != "" {
			fmt.Fprintln(p.Err, prefix+r.Message)
		}
	}
}

func fit(s string, w int) string {
	if len(s) > w {
		s = s[:w-3] + "..."
	}
	return fmt.Sprintf("%-*s", w, s)
}

func PrintSummary(w io.Writer, s *engine.Summary) {
	fmt.Fprintf(w, "\n%-8s %s %s %-8s %-11s %-6s %s\n", "Platform", fit("Feature", 14), fit("Flow", 42), "Status", "Stage", "Exit", "Duration")
	fmt.Fprintln(w, strings.Repeat("-", 94))
	for _, r := range s.Results {
		status := "PASS"
		if !r.Passed() {
			status = "FAIL"
		}
		fmt.Fprintf(w, "%-8s %s %s %-8s %-11s %-6d %ds\n", r.Platform, fit(r.Feature, 14), fit(r.File, 42), status, r.Stage, r.Exit, int(r.Duration.Seconds()))
	}
	fmt.Fprintln(w, strings.Repeat("-", 94))
	t := int(s.Total.Seconds())
	fmt.Fprintf(w, "Total: %d, %d pass / %d fail (%dm %ds)\n", len(s.Results), s.Passed, s.Failed, t/60, t%60)
}

func PrintFailedReruns(w io.Writer, p *project.Project, s *engine.Summary) {
	byPlatform := map[string][]string{}
	for _, r := range s.Results {
		if r.Passed() {
			continue
		}
		path := r.Path
		if path == "" {
			path = filepath.Join(p.FlowsRoot(), r.Feature, r.File)
		}
		byPlatform[r.Platform] = append(byPlatform[r.Platform], shellQuote(p.Rel(path)))
	}
	if len(byPlatform) == 0 {
		return
	}
	fmt.Fprintln(w, "\nTo re-run the failed flow/platform pairs:")
	for _, name := range []string{"", "android", "ios"} {
		if paths := byPlatform[name]; len(paths) > 0 {
			flag := ""
			if name != "" {
				flag = " --platform " + name
			}
			fmt.Fprintf(w, "  ./mockstrow run%s %s\n", flag, strings.Join(paths, " "))
		}
	}
}

func PrintRecordings(w io.Writer, p *project.Project, s *engine.Summary) {
	printed := false
	for _, r := range s.Results {
		if r.Recording == "" {
			continue
		}
		if !printed {
			fmt.Fprintln(w, "\nRecordings:")
			printed = true
		}
		fmt.Fprintf(w, "  %s/%s: %s\n", r.Feature, r.File, p.Rel(r.Recording))
	}
}

func shellQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " '\"$`\\!*?[]{}();&|<>") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
