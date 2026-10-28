package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/javaruntime"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

// Check is one line of the page's setup panel.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // ok | warn | fail | info
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// noTrafficAfter is how long a running mock may sit without a request before the page suggests
// the app is not reaching it.
const noTrafficAfter = 20 * time.Second

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	checks := s.runChecks(ctx)
	overall := "ok"
	for _, c := range checks {
		if c.Status == "fail" {
			overall = "fail"
		} else if c.Status == "warn" && overall == "ok" {
			overall = "warn"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"overall": overall, "checks": checks})
}

func (s *Server) runChecks(ctx context.Context) []Check {
	p, m := s.ws.Current()
	if p == nil || m == nil {
		return []Check{{ID: "project", Title: "Project", Status: "fail", Detail: "No project folder is chosen yet.", Fix: "Choose the project folder (the one that contains .maestro)."}}
	}
	t := m.proxyTargets()
	state := m.State()
	serving := state.State == StateServing

	checks := []Check{s.checkProject(p), s.checkJava(ctx), s.checkJar(), s.checkPort(ctx, t.port, serving)}
	if t.lite {
		checks = append(checks, s.checkCharlesRunning(ctx), s.checkCharlesRules(t), s.checkMacProxy(ctx))
	}
	checks = append(checks, s.checkTraffic(m, serving))
	return checks
}

func (s *Server) checkProject(p *project.Project) Check {
	counts, err := fixtures.New(p).ValidateAll(false)
	if err != nil {
		return Check{ID: "project", Title: "Mock files", Status: "fail", Detail: err.Error(), Fix: "The mock files have an error. Tell a developer."}
	}
	return Check{ID: "project", Title: "Mock files", Status: "ok",
		Detail: fmt.Sprintf("%d stubs, %d customer profiles and %d scenarios, all valid.", counts.Stubs, counts.Profiles, counts.Scenarios)}
}

var javaVersion = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

// parseJavaMajor reads the major version from `java -version` output (1.8 counts as 8).
func parseJavaMajor(out string) (int, bool) {
	m := javaVersion.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	major, _ := strconv.Atoi(m[1])
	if major == 1 && m[2] != "" {
		major, _ = strconv.Atoi(m[2])
	}
	return major, true
}

func (s *Server) checkJava(ctx context.Context) Check {
	c := Check{ID: "java", Title: "Java", Fix: javaruntime.Hint}
	path, err := s.ex.LookPath("java")
	if err != nil {
		c.Status, c.Detail = "fail", "Java is not installed."
		return c
	}
	// `java -version` writes to stderr, so merge it into the output.
	out, err := s.ex.Output(ctx, execx.Cmd{Name: "sh", Args: []string{"-c", `"$0" -version 2>&1`, path}})
	major, ok := parseJavaMajor(out)
	if err != nil || !ok {
		c.Status, c.Detail = "fail", "Java is not installed (macOS found only its placeholder)."
		return c
	}
	if major < 17 {
		c.Status, c.Detail = "warn", fmt.Sprintf("Java %d is installed; version 17 or newer is expected.", major)
		return c
	}
	c.Status, c.Detail, c.Fix = "ok", fmt.Sprintf("Java %d", major), ""
	return c
}

func (s *Server) checkJar() Check {
	if _, err := os.Stat(wiremock.JarPath()); err == nil {
		return Check{ID: "jar", Title: "Mock server program", Status: "ok", Detail: "Downloaded and ready."}
	}
	return Check{ID: "jar", Title: "Mock server program", Status: "info", Detail: "It will download the first time you start a mock (about 15 MB, needs internet)."}
}

func (s *Server) checkPort(ctx context.Context, port int, serving bool) Check {
	c := Check{ID: "port", Title: fmt.Sprintf("Port %d", port)}
	if serving {
		c.Status, c.Detail = "ok", "In use by the mock you started."
		return c
	}
	if who := wiremock.PortListener(ctx, s.ex, port); who != "" {
		c.Status, c.Detail = "fail", fmt.Sprintf("%s is already using it.", who)
		c.Fix = "Close the other Mockstro or Maestro run. If nothing is open, run: mockstro sweep"
		return c
	}
	c.Status, c.Detail = "ok", "Free."
	return c
}

func (s *Server) checkCharlesRunning(ctx context.Context) Check {
	c := Check{ID: "charles", Title: "Charles"}
	if _, err := s.ex.Output(ctx, execx.Cmd{Name: "pgrep", Args: []string{"-f", "Charles.app/Contents"}}); err != nil {
		c.Status, c.Detail, c.Fix = "warn", "Charles is not running.", "Open Charles before testing the app."
		return c
	}
	c.Status, c.Detail = "ok", "Running."
	return c
}

func (s *Server) checkCharlesRules(t proxyTargets) Check {
	c := Check{ID: "rules", Title: "Charles rules"}
	f, err := os.Open(s.charlesConfig)
	if err != nil {
		c.Status, c.Detail = "info", "Could not read Charles's saved settings, so the rules were not checked."
		return c
	}
	defer f.Close()
	rules, err := readMapRemote(f)
	if err != nil {
		c.Status, c.Detail = "info", "Charles has no Map Remote rules saved yet."
		c.Fix = "In Charles: Tools, Map Remote, then add the rows from Charles setup below."
		return c
	}
	port := strconv.Itoa(t.port)
	apiStatus, apiDetail := mapVerdict(rules, t.api, port)
	fbStatus, fbDetail := mapVerdict(rules, t.fbrc, port)
	switch {
	case apiStatus != "ok":
		c.Status, c.Detail = apiStatus, apiDetail
		c.Fix = "In Charles: Tools, Map Remote. Use the rows from Charles setup below."
	case fbStatus != "ok":
		c.Status, c.Detail = "warn", "The API is mapped, but Remote Config is not: "+strings.ToLower(fbDetail[:1])+fbDetail[1:]+". Remote Config values will not reach the app."
		c.Fix = "Add the Firebase row from Charles setup below."
	default:
		c.Status, c.Detail = "ok", "The API and Remote Config both go to the mock."
	}
	return c
}

// parseScutilProxy reads the key : value lines of `scutil --proxy`.
func parseScutilProxy(out string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, " : ")
		if ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return kv
}

func (s *Server) checkMacProxy(ctx context.Context) Check {
	c := Check{ID: "macproxy", Title: "Mac proxy"}
	out, err := s.ex.Output(ctx, execx.Cmd{Name: "scutil", Args: []string{"--proxy"}})
	if err != nil {
		c.Status, c.Detail = "info", "Could not read the Mac's proxy settings."
		return c
	}
	kv := parseScutilProxy(out)
	host, port := kv["HTTPSProxy"], kv["HTTPSPort"]
	if kv["HTTPSEnable"] != "1" || (host != "127.0.0.1" && host != "localhost") {
		c.Status, c.Detail = "warn", "The Mac is not sending web traffic through a local proxy, so the simulator's requests will not reach Charles."
		c.Fix = "In Charles: Proxy menu, turn on macOS Proxy."
		return c
	}
	c.Status, c.Detail = "ok", "Web traffic goes through "+host+":"+port+"."
	return c
}

func (s *Server) checkTraffic(m *Manager, serving bool) Check {
	c := Check{ID: "traffic", Title: "App traffic"}
	if !serving {
		c.Status, c.Detail = "info", "Start a mock to see whether the app reaches it."
		return c
	}
	count, unmatched, age := m.trafficSummary()
	switch {
	case count == 0 && age > noTrafficAfter:
		c.Status, c.Detail = "warn", "The app has not sent anything to the mock yet."
		c.Fix = "Open the app. If it was already open, close it and open it again. Check the Charles rows above."
	case count == 0:
		c.Status, c.Detail = "info", "Waiting for the app to send a request."
	case unmatched > 0:
		c.Status, c.Detail = "ok", fmt.Sprintf("%d requests reached the mock; %d had no stub (shown in red on the Session page).", count, unmatched)
	default:
		c.Status, c.Detail = "ok", fmt.Sprintf("%d requests reached the mock.", count)
	}
	return c
}
