package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

// fakeMaestro plays a batched `maestro test`: it calls each flow's hooks like Maestro would,
// skipping the final "passed" step for flows whose file name contains "fail".
func fakeMaestro(stopAfter int) func(c execx.Cmd) (string, error) {
	return fakeMaestroWith(stopAfter, false, "")
}

// fakeMaestroWith can skip completion for failing flows (their own onFlowComplete failed) and,
// with results set, writes each flow's output dir after it ends, as Maestro does.
func fakeMaestroWith(stopAfter int, skipFailedComplete bool, results string) func(c execx.Cmd) (string, error) {
	return func(c execx.Cmd) (string, error) {
		ran := 0
		var mlog *os.File
		for i, a := range c.Args {
			if i > 0 && c.Args[i-1] == "--debug-output" {
				dir := filepath.Join(a, ".maestro", "tests", "ts")
				_ = os.MkdirAll(dir, 0o755)
				mlog, _ = os.Create(filepath.Join(dir, "maestro.log"))
				defer mlog.Close()
			}
		}
		for i, a := range c.Args {
			if !strings.HasSuffix(a, ".yaml") || c.Args[i-1] == "--config" {
				continue
			}
			if ran == stopAfter {
				return "", errors.New("maestro crashed")
			}
			ran++
			data, _ := os.ReadFile(a)
			var h struct {
				OnFlowStart []struct {
					RunScript struct{ Env map[string]string } `yaml:"runScript"`
				} `yaml:"onFlowStart"`
			}
			header, _, _ := strings.Cut(string(data), "\n---\n")
			if err := yaml.Unmarshal([]byte(header), &h); err != nil || len(h.OnFlowStart) == 0 {
				return "", fmt.Errorf("no hooks in %s: %v", a, err)
			}
			env := h.OnFlowStart[0].RunScript.Env
			call := func(action string) int {
				resp, err := http.Post(env["MOCKSTRO_URL"]+"/"+action+"?flow="+url.QueryEscape(env["MOCKSTRO_FLOW"]), "", nil)
				if err != nil {
					return 0
				}
				resp.Body.Close()
				return resp.StatusCode
			}
			if results != "" {
				_ = os.MkdirAll(filepath.Join(results, "ts", fmt.Sprintf("same-name-%d", ran), "screenshots"), 0o755)
			}
			if call("start") == http.StatusConflict {
				continue
			}
			fmt.Fprintf(c.Stdout, "output of %s\n", filepath.Base(a))
			if mlog != nil {
				fmt.Fprintf(mlog, "12:00:00.000 [ INFO] maestro.cli.runner.CliConsoleListener.onCommandFinished: Apply configuration COMPLETED\n")
				fmt.Fprintf(mlog, "12:00:00.000 [ INFO] maestro.cli.runner.CliConsoleListener.onCommandFinished: Tap on \"%s\" COMPLETED\n", filepath.Base(a))
			}
			if !strings.Contains(a, "fail") {
				call("passed")
			}
			if !skipFailedComplete || !strings.Contains(a, "fail") {
				call("complete")
			}

		}
		return "", nil
	}
}

func batchRun(t *testing.T, h func(c execx.Cmd) (string, error), names ...string) (*Run, []*fixtures.Flow) {
	t.Helper()
	e, _ := testEngine(t, "agent", h)
	r := e.newRun("run", 0)
	if err := r.initDir(); err != nil {
		t.Fatal(err)
	}
	wm := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == wiremock.NoncePath:
			io.WriteString(w, r.ID)
		case req.URL.Path == "/__admin/requests":
			io.WriteString(w, `{"requests":[]}`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wm.Listener = ln
	wm.Start()
	t.Cleanup(wm.Close)
	r.Port = ln.Addr().(*net.TCPAddr).Port
	r.WM = wiremock.Connect(r.Port, r.ID)

	profiles := e.P.ProfilesRoot()
	_ = os.MkdirAll(profiles, 0o755)
	if err := os.WriteFile(filepath.Join(profiles, "shared.json"), []byte(`{"mappings":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var flows []*fixtures.Flow
	for _, n := range names {
		path := filepath.Join(e.P.FlowsRoot(), "f", n+".yaml")
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte("appId: ${APP_ID}\nname: "+n+"\n---\n- launchApp\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		hd, err := fixtures.ParseHeader(path)
		if err != nil {
			t.Fatal(err)
		}
		flows = append(flows, &fixtures.Flow{Path: path, Rel: "f/" + n + ".yaml", Feature: "f", File: n + ".yaml", Header: hd})
	}
	return r, flows
}

func runBatch(r *Run, flows []*fixtures.Flow) ([]*Result, bool) {
	results := make([]*Result, len(flows))
	halted := r.RunBatch(context.Background(), flows, nil, func(i int, res *Result) { results[i] = res })
	return results, halted
}

func TestRunBatchReportsEachFlowFromOneMaestro(t *testing.T) {
	r, flows := batchRun(t, fakeMaestro(-1), "ok", "fail", "ok2")
	results, halted := runBatch(r, flows)
	if halted {
		t.Fatal("halted without --fail-fast")
	}
	var got []string
	for _, res := range results {
		got = append(got, res.Stage)
	}
	if strings.Join(got, ",") != "PASS,TEST,PASS" {
		t.Fatalf("stages %v: %s", got, results[0].Message)
	}
	log, _ := os.ReadFile(results[1].LogPath)
	if strings.TrimSpace(string(log)) != "output of .mockstro-batch-fail.yaml\nTap on \".mockstro-batch-fail.yaml\"... COMPLETED" {
		t.Fatalf("flow log should hold only its own output, got %q", log)
	}
	n := 0
	for _, c := range r.e.Exec.(*execx.Fake).Calls {
		if strings.HasPrefix(c, "maestro ") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one Maestro process, got %d", n)
	}
}

func TestRunBatchFailFastStopsAtFirstFailure(t *testing.T) {
	r, flows := batchRun(t, fakeMaestro(-1), "fail", "ok")
	r.e.Opts.FailFast = true
	results, halted := runBatch(r, flows)
	if !halted || results[0] == nil || results[0].Passed() || results[1] != nil {
		t.Fatalf("halted %v results %v", halted, results)
	}
}

func TestRunBatchMaestroExitReportsUnfinishedFlows(t *testing.T) {
	r, flows := batchRun(t, fakeMaestro(1), "ok", "never")
	results, _ := runBatch(r, flows)
	if results[0].Stage != "PASS" {
		t.Fatalf("first flow %s", results[0].Stage)
	}
	if results[1] == nil || results[1].Stage != "TEST" || !strings.Contains(results[1].Message, "before the flow started") {
		t.Fatalf("unstarted flow must fail visibly, got %+v", results[1])
	}
}

func TestClaimBatchSharesStartupAndBalances(t *testing.T) {
	flows := make([]*fixtures.Flow, 10)
	for i := range flows {
		flows[i] = &fixtures.Flow{File: fmt.Sprint(i)}
	}
	q := &queue{flows: flows, claimed: make([]bool, 10), results: make([]*Result, 10), workers: 2}
	var sizes []int
	for {
		idx := q.claimBatch(q.workers)
		if len(idx) == 0 {
			break
		}
		sizes = append(sizes, len(idx))
	}
	if fmt.Sprint(sizes) != "[3 2 2 1 1 1]" {
		t.Fatalf("chunk sizes %v", sizes)
	}
}


func TestRunBatchFailFastWhenCompletionHookNeverRuns(t *testing.T) {
	r, flows := batchRun(t, fakeMaestroWith(-1, true, ""), "fail", "ok")
	r.e.Opts.FailFast = true
	results, halted := runBatch(r, flows)
	if !halted || results[0] == nil || results[0].Passed() || results[1] != nil {
		t.Fatalf("halted %v results %v", halted, results)
	}
}

func TestRunBatchStopsWhenAnotherWorkerHalted(t *testing.T) {
	r, flows := batchRun(t, fakeMaestro(-1), "ok", "ok2")
	stopped := false
	results := make([]*Result, 2)
	halted := r.RunBatch(context.Background(), flows, func() bool { return stopped }, func(i int, res *Result) {
		results[i] = res
		stopped = true
	})
	if !halted || results[0] == nil || results[1] != nil {
		t.Fatalf("halted %v results %v", halted, results)
	}
}

func TestRunBatchAssignsEachFlowItsOwnOutputDir(t *testing.T) {
	r, flows := batchRun(t, nil, "a", "b")
	r.e.Exec.(*execx.Fake).Handler = fakeMaestroWith(-1, false, filepath.Join(r.Dir, "results"))
	results, _ := runBatch(r, flows)
	if filepath.Base(results[0].Artifacts) != "same-name-1" || filepath.Base(results[1].Artifacts) != "same-name-2" {
		t.Fatalf("artifacts %q %q", results[0].Artifacts, results[1].Artifacts)
	}
}

func TestBatchKeepsSubflowCopiesPlainAndConfigUnfiltered(t *testing.T) {
	var cfg string
	var plain []byte
	r, flows := batchRun(t, nil, "a")
	r.e.Exec.(*execx.Fake).Handler = func(c execx.Cmd) (string, error) {
		for i, a := range c.Args {
			if i > 0 && c.Args[i-1] == "--config" {
				b, _ := os.ReadFile(a)
				cfg = string(b)
			}
		}
		plain, _ = os.ReadFile(filepath.Join(r.flowsRoot, "flows", "f", "a.yaml"))
		return fakeMaestro(-1)(c)
	}
	if err := os.WriteFile(r.configPath, []byte("includeTags: [smoke]\nflows: [x/**]\nexecutionOrder: {continueOnFailure: false}\nplatform: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	results, _ := runBatch(r, flows)
	if results[0].Stage != "PASS" {
		t.Fatalf("stage %s: %s", results[0].Stage, results[0].Message)
	}
	for _, k := range []string{"includeTags", "flows:", "executionOrder"} {
		if strings.Contains(cfg, k) {
			t.Fatalf("batch config keeps %s:\n%s", k, cfg)
		}
	}
	if !strings.Contains(cfg, "platform") || !strings.Contains(cfg, "testOutputDir") {
		t.Fatalf("batch config lost settings:\n%s", cfg)
	}
	if len(plain) == 0 || strings.Contains(string(plain), "MOCKSTRO") {
		t.Fatalf("the copy other flows call must have no hooks:\n%s", plain)
	}
}
