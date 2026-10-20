package engine

import (
	"fmt"
	"path/filepath"
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
	Platform string
	Kind     EventKind
	Time     time.Time
	Worker   int // 0 = single run / parallel parent
	RunID    string
	Level    string // info | warn | error
	Text     string
	Stage    string
	Flow     string
	Device   string
	Port     int
	Result   *Result
	Summary  *Summary
}

type Sink func(Event)

type VerificationResult struct {
	Stub        string
	Expectation string
	Got         int
	Pass        bool
}

type Result struct {
	Platform  string               `json:"platform"`
	Feature   string               `json:"feature"`
	File      string               `json:"file"`
	Path      string               `json:"path"`
	Stage     string               `json:"stage"` // PASS TEST VERIFY OVERRIDE CANCELLED RECORD OPTIMIZE INTERRUPTED UNSTARTED SETUP
	Exit      int                  `json:"exit"`
	Duration  time.Duration        `json:"duration"`
	Recording string               `json:"recording"`
	Worker    int                  `json:"worker"`
	Device    string               `json:"device"`
	LogPath   string               `json:"logPath"`
	Artifacts string               `json:"artifacts"` // Maestro's per-flow output: screenshots, screen hierarchy, logcat
	Shots     []string             `json:"shots"`     // screenshots Maestro captured (the last one is usually the failure)
	Verifs    []VerificationResult `json:"verifs"`
	Unmatched []string             `json:"unmatched"`
	Message   string               `json:"message"`
}

func (r *Result) Passed() bool { return r.Stage == "PASS" && r.Exit == 0 }

// TSV is a result.tsv line: feature|flow|stage|exit|<n>s|recording.
func (r *Result) TSV() string {
	return strings.Join([]string{r.Feature, r.File, r.Stage, strconv.Itoa(r.Exit), fmt.Sprintf("%ds", int(r.Duration.Seconds())), r.Recording, r.Platform, r.Path}, "|")
}

func ParseResultTSV(line string) *Result {
	f := strings.Split(line, "|")
	for len(f) < 8 {
		f = append(f, "")
	}
	exit, _ := strconv.Atoi(f[3])
	secs, _ := strconv.Atoi(strings.TrimSuffix(f[4], "s"))
	return &Result{Platform: f[6], Path: f[7], Feature: f[0], File: f[1], Stage: f[2], Exit: exit, Duration: time.Duration(secs) * time.Second, Recording: f[5]}
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

// Key distinguishes the same flow executed on different platforms.
func (r *Result) Key() string {
	key := r.Feature + "/" + r.File
	if r.Platform != "" {
		return r.Platform + "/" + key
	}
	return key
}

// FailedPairs selects only unsuccessful flow/platform results, preserving other options.
func FailedPairs(o Options, results []*Result, root string) Options {
	o.Only = map[string][]string{}
	o.FlowArgs = nil
	for _, r := range results {
		if r.Passed() {
			continue
		}
		path := r.Path
		if path == "" {
			path = filepath.Join(root, ".maestro", "flows", r.Feature, r.File)
		}
		platform := r.Platform
		if platform == "" {
			platform = o.Platform
		}
		o.Only[platform] = append(o.Only[platform], path)
	}
	return o
}
