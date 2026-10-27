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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/mockruntime"
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
		var outputRoot string
		var mlog *os.File
		for i, a := range c.Args {
			if i > 0 && c.Args[i-1] == "--debug-output" {
				dir := a
				outputRoot = a
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
				_ = os.MkdirAll(filepath.Join(outputRoot, fmt.Sprintf("same-name-%d", ran), "screenshots"), 0o755)
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
	return batchRunObserved(t, h, nil, names...)
}

func batchRunObserved(t *testing.T, h func(execx.Cmd) (string, error), observe func(*http.Request), names ...string) (*Run, []*fixtures.Flow) {
	t.Helper()
	e, _ := testEngine(t, "agent", h)
	e.Exec.(*execx.Fake).Paths["maestro"] = "maestro"
	r := e.newRun("run", 0)
	if err := r.initDir(); err != nil {
		t.Fatal(err)
	}
	wm := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if observe != nil {
			observe(req)
		}
		switch {
		case req.URL.Path == wiremock.NoncePath:
			io.WriteString(w, r.ID)
		case req.Method == http.MethodPost && req.URL.Path == "/__admin/mappings":
			io.WriteString(w, `{"id":"custom"}`)
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
	var mu sync.Mutex
	imports, journalResets := 0, 0
	r, flows := batchRunObserved(t, fakeMaestro(1), func(req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if req.URL.Path == "/__admin/mappings/import" {
			imports++
		}
		if req.Method == http.MethodDelete && req.URL.Path == "/__admin/requests" {
			journalResets++
		}
	}, "ok", "never")
	results, _ := runBatch(r, flows)
	if results[0].Stage != "PASS" {
		t.Fatalf("first flow %s", results[0].Stage)
	}
	if results[1] == nil || results[1].Stage != "TEST" || !strings.Contains(results[1].Message, "before the flow started") {
		t.Fatalf("unstarted flow must fail visibly, got %+v", results[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if imports != 1 || journalResets != 1 {
		t.Fatalf("unreached flow changed fixtures: imports=%d journalResets=%d", imports, journalResets)
	}
}

func TestRunBatchSetupFailureDoesNotActivateAnyFlow(t *testing.T) {
	var mu sync.Mutex
	imports := 0
	r, flows := batchRunObserved(t, fakeMaestro(-1), func(req *http.Request) {
		if req.URL.Path == "/__admin/mappings/import" {
			mu.Lock()
			imports++
			mu.Unlock()
		}
	}, "one", "two")
	if err := os.WriteFile(r.configPath, []byte("invalid: ["), 0644); err != nil {
		t.Fatal(err)
	}
	results, _ := runBatch(r, flows)
	for _, res := range results {
		if res == nil || res.Stage != "SETUP" {
			t.Fatalf("setup outcome: %+v", res)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if imports != 0 {
		t.Fatalf("setup failure activated %d flows", imports)
	}
}

func TestIsolatedCancellationStopsWorkerWithoutCancellingParent(t *testing.T) {
	for _, code := range []int{130, 143} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			exit := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
			if execx.ExitCode(exit) != code {
				t.Fatalf("exit fixture: %v", exit)
			}
			launched := 0
			r, flows := batchRun(t, func(c execx.Cmd) (string, error) {
				if c.Name == "maestro" {
					launched++
					return "", exit
				}
				return "", nil
			}, "cancelled", "never")
			r.e.Opts.Isolate = true
			ctx := context.Background()
			results, failures := r.e.executeFlows(ctx, flows, 1, r.Dir, func(context.Context, int) (*Run, error) { return r, nil })
			if ctx.Err() != nil || len(failures) != 0 || launched != 1 || results[0].Stage != "CANCELLED" || results[1].Stage != "UNSTARTED" {
				t.Fatalf("launched=%d results=%+v,%+v failures=%v", launched, results[0], results[1], failures)
			}
		})
	}
}

func TestFailedSwitchReconcilesCustomResponsesAfterMappingReplacement(t *testing.T) {
	for _, afterReplacement := range []bool{false, true} {
		t.Run(fmt.Sprint(afterReplacement), func(t *testing.T) {
			r, _ := batchRun(t, nil, "unused")
			r.Device, r.overrideAuto = "device", true
			failure := errors.New("device override failed")
			r.e.Plat = &deliveryPlatform{err: failure}
			runtime := &mockRuntime{run: r, done: make(chan struct{})}
			session := mockruntime.New(runtime, &mockruntime.Target{Name: "old", FBRC: map[string]string{"K": "old"}}, r.Port, r.Device)
			if _, err := session.SetCustomResponse(mockruntime.CustomResponse{Method: "GET", Path: "/custom", Status: 503}); err != nil {
				t.Fatal(err)
			}
			next := &mockruntime.Target{Name: "new", FBRC: map[string]string{"K": "new"}}
			if !afterReplacement {
				next.Profile = "missing-profile"
			}
			err := session.Switch(context.Background(), next)
			if err == nil || (afterReplacement && !errors.Is(err, failure)) {
				t.Fatalf("switch error: %v", err)
			}
			want := 1
			if afterReplacement {
				want = 0
			}
			if got := len(session.CustomResponses()); got != want {
				t.Fatalf("custom responses=%d, want %d", got, want)
			}
			if snap := session.Snapshot(); snap.Name != "old" || snap.FBRC["K"] != "old" {
				t.Fatalf("failed switch published target: %+v", snap)
			}
		})
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
	if !strings.Contains(cfg, "platform") {
		t.Fatalf("batch config lost settings:\n%s", cfg)
	}
	if len(plain) == 0 || strings.Contains(string(plain), "MOCKSTRO") {
		t.Fatalf("the copy other flows call must have no hooks:\n%s", plain)
	}
}
