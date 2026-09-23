package engine

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type EventKind int

const (
	EvLog         EventKind = iota // engine message
	EvOutput                       // raw line from maestro / gradle / xcodebuild
	EvStage                        // a run changed stage
	EvWorker                       // a worker acquired its device/port
	EvFlowStart                    // a flow began
	EvFlowResult                   // a flow finished
	EvSessionDone                  // everything finished; Summary set
)

type Event struct {
	Kind    EventKind
	Time    time.Time
	Worker  int // 0 = single run / parallel parent
	RunID   string
	Level   string // info | warn | error
	Text    string
	Stage   string
	Flow    string
	Device  string
	Port    int
	Result  *Result
	Summary *Summary
}

type Sink func(Event)

type VerificationResult struct {
	Stub        string
	Expectation string
	Got         int
	Pass        bool
}

type Result struct {
	Feature   string
	File      string
	Path      string
	Stage     string // PASS TEST VERIFY OVERRIDE CANCELLED RECORD OPTIMIZE INTERRUPTED UNSTARTED SETUP
	Exit      int
	Duration  time.Duration
	Recording string
	Worker    int
	Device    string
	LogPath   string
	Artifacts string   // Maestro's per-flow output: screenshots, screen hierarchy, logcat
	Shots     []string // screenshots Maestro captured (the last one is usually the failure)
	Verifs    []VerificationResult
	Unmatched []string
	Message   string
}

func (r *Result) Passed() bool { return r.Stage == "PASS" && r.Exit == 0 }

// TSV is test.sh's result line: feature|flow|stage|exit|<n>s|recording.
func (r *Result) TSV() string {
	return strings.Join([]string{r.Feature, r.File, r.Stage, strconv.Itoa(r.Exit), fmt.Sprintf("%ds", int(r.Duration.Seconds())), r.Recording}, "|")
}

func ParseResultTSV(line string) *Result {
	f := strings.Split(line, "|")
	for len(f) < 6 {
		f = append(f, "")
	}
	exit, _ := strconv.Atoi(f[3])
	secs, _ := strconv.Atoi(strings.TrimSuffix(f[4], "s"))
	return &Result{Feature: f[0], File: f[1], Stage: f[2], Exit: exit, Duration: time.Duration(secs) * time.Second, Recording: f[5]}
}

type Summary struct {
	Results   []*Result
	Total     time.Duration
	Dir       string // run or session dir
	Failed    int
	Passed    int
	Cancelled bool
	Err       error
}

func NewSummary(results []*Result, total time.Duration, dir string) *Summary {
	s := &Summary{Results: results, Total: total, Dir: dir}
	for _, r := range results {
		if r.Passed() {
			s.Passed++
		} else {
			s.Failed++
		}
		if r.Stage == "CANCELLED" {
			s.Cancelled = true
		}
	}
	return s
}
