package web

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
)

func TestParseJavaMajor(t *testing.T) {
	for out, want := range map[string]int{
		`openjdk version "17.0.10" 2024-01-16`:     17,
		`java version "1.8.0_292"`:                 8,
		`openjdk version "21" 2023-09-19`:          21,
		`openjdk version "11.0.22" 2024-01-16 LTS`: 11,
	} {
		if got, ok := parseJavaMajor(out); !ok || got != want {
			t.Errorf("%q = %d %v, want %d", out, got, ok, want)
		}
	}
	if _, ok := parseJavaMajor("The operation couldn't be completed. Unable to locate a Java Runtime."); ok {
		t.Error("the macOS placeholder message is not a version")
	}
}

func TestParseScutilProxy(t *testing.T) {
	kv := parseScutilProxy("<dictionary> {\n  HTTPSEnable : 1\n  HTTPSPort : 8888\n  HTTPSProxy : 127.0.0.1\n}\n")
	if kv["HTTPSEnable"] != "1" || kv["HTTPSPort"] != "8888" || kv["HTTPSProxy"] != "127.0.0.1" {
		t.Fatalf("kv = %v", kv)
	}
}

const charlesConfig = `<?xml version='1.0' encoding='UTF-8' ?>
<configuration>
  <toolConfiguration>
    <configs>
      <entry><string>Block List</string><blacklist /></entry>
      <entry>
        <string>Map Remote</string>
        <map>
          <toolEnabled>%s</toolEnabled>
          <mappings>%s</mappings>
        </map>
      </entry>
    </configs>
  </toolConfiguration>
</configuration>`

func rule(srcHost, srcPort, destHost, destPort string, enabled bool) string {
	e := ""
	if !enabled {
		e = "<enabled>false</enabled>"
	}
	return `<mapMapping><sourceLocation><protocol>https</protocol><host>` + srcHost + `</host><port>` + srcPort + `</port></sourceLocation>` +
		`<destLocation><protocol>http</protocol><host>` + destHost + `</host><port>` + destPort + `</port></destLocation>` + e + `</mapMapping>`
}

func TestMapVerdict(t *testing.T) {
	api, fb := "https://api.example.test:2015", "https://firebaseremoteconfig.googleapis.com"
	cases := []struct {
		name  string
		tool  string
		rules []string
		url   string
		want  string
	}{
		{"wildcard host and port", "true", []string{rule("*.example.test", "*", "127.0.0.1", "18080", true)}, api, "ok"},
		{"exact rule", "true", []string{rule("api.example.test", "2015", "127.0.0.1", "18080", true)}, api, "ok"},
		{"wrong port in rule", "true", []string{rule("api.example.test", "443", "127.0.0.1", "18080", true)}, api, "fail"},
		{"no rule", "true", []string{rule("other.example", "*", "127.0.0.1", "18080", true)}, api, "fail"},
		{"disabled rule is skipped", "true", []string{rule("*.example.test", "*", "127.0.0.1", "18080", false)}, api, "fail"},
		{"mapped to another place", "true", []string{rule("*.example.test", "*", "127.0.0.1", "9999", true)}, api, "fail"},
		{"first matching rule wins", "true", []string{rule("*.example.test", "*", "10.0.0.5", "80", true), rule("*.example.test", "*", "127.0.0.1", "18080", true)}, api, "fail"},
		{"later rule used when earlier does not match", "true", []string{rule("nope.example", "*", "10.0.0.5", "80", true), rule("*.example.test", "*", "127.0.0.1", "18080", true)}, api, "ok"},
		{"tool switched off", "false", []string{rule("*.example.test", "*", "127.0.0.1", "18080", true)}, api, "fail"},
		{"firebase default port", "true", []string{rule("firebaseremoteconfig.googleapis.com", "443", "localhost", "18080", true)}, fb, "ok"},
	}
	for _, c := range cases {
		m, err := readMapRemote(strings.NewReader(strings.Replace(strings.Replace(charlesConfig, "%s", c.tool, 1), "%s", strings.Join(c.rules, ""), 1)))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got, detail := mapVerdict(m, c.url, "18080"); got != c.want {
			t.Errorf("%s: %s (%s), want %s", c.name, got, detail, c.want)
		}
	}
}

func TestReadMapRemoteWithoutTheSection(t *testing.T) {
	if _, err := readMapRemote(strings.NewReader(`<configuration><entry><string>Other</string><map></map></entry></configuration>`)); err == nil {
		t.Fatal("expected an error when there is no Map Remote entry")
	}
}

type tools struct {
	java, lsof, pgrep, scutil string
	javaErr, pgrepErr         error
	noJava                    bool
}

func healthServer(t *testing.T, tl tools, charles string) *Server {
	t.Helper()
	paths := map[string]string{"lsof": "/usr/sbin/lsof"}
	if !tl.noJava {
		paths["java"] = "/usr/bin/java"
	}
	ex := &execx.Fake{Paths: paths, Handler: func(c execx.Cmd) (string, error) {
		switch c.Name {
		case "sh":
			return tl.java, tl.javaErr
		case "lsof":
			return tl.lsof, nil
		case "pgrep":
			return tl.pgrep, tl.pgrepErr
		case "scutil":
			return tl.scutil, nil
		}
		return "", errors.New("unexpected " + c.String())
	}}
	s, _ := testServer(t)
	s.ex = ex
	if charles != "" {
		path := filepath.Join(t.TempDir(), "charles.config")
		if err := os.WriteFile(path, []byte(charles), 0o644); err != nil {
			t.Fatal(err)
		}
		s.charlesConfig = path
	} else {
		s.charlesConfig = filepath.Join(t.TempDir(), "missing")
	}
	return s
}

func checks(t *testing.T, s *Server) (map[string]Check, string) {
	t.Helper()
	rec := do(s, "GET", "/api/health", goodHost, nil, "")
	var out struct {
		Overall string
		Checks  []Check
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	m := map[string]Check{}
	for _, c := range out.Checks {
		m[c.ID] = c
	}
	return m, out.Overall
}

var goodTools = tools{
	java:   `openjdk version "17.0.10" 2024-01-16`,
	pgrep:  "4242",
	scutil: "<dictionary> {\n  HTTPSEnable : 1\n  HTTPSPort : 8888\n  HTTPSProxy : 127.0.0.1\n}",
}

func goodCharles() string {
	return strings.Replace(strings.Replace(charlesConfig, "%s", "true", 1), "%s",
		rule("api.example.test", "443", "127.0.0.1", "18080", true)+rule("firebaseremoteconfig.googleapis.com", "443", "127.0.0.1", "18080", true), 1)
}

func TestHealthAllGood(t *testing.T) {
	c, overall := checks(t, healthServer(t, goodTools, goodCharles()))
	for id, want := range map[string]string{"java": "ok", "port": "ok", "charles": "ok", "rules": "ok", "macproxy": "ok", "traffic": "info"} {
		if c[id].Status != want {
			t.Errorf("%s = %s (%s), want %s", id, c[id].Status, c[id].Detail, want)
		}
	}
	if overall == "fail" {
		t.Errorf("overall = %s", overall)
	}
}

func TestHealthJava(t *testing.T) {
	c, overall := checks(t, healthServer(t, tools{noJava: true, pgrep: "1", scutil: goodTools.scutil}, goodCharles()))
	if c["java"].Status != "fail" || !strings.Contains(c["java"].Fix, "brew install openjdk@17") || overall != "fail" {
		t.Errorf("missing java: %+v overall %s", c["java"], overall)
	}
	stub := goodTools
	stub.java, stub.javaErr = "The operation couldn't be completed. Unable to locate a Java Runtime.", errors.New("exit 1")
	if c, _ := checks(t, healthServer(t, stub, goodCharles())); c["java"].Status != "fail" {
		t.Errorf("macOS placeholder java: %+v", c["java"])
	}
	old := goodTools
	old.java = `openjdk version "11.0.22" 2024-01-16`
	if c, _ := checks(t, healthServer(t, old, goodCharles())); c["java"].Status != "warn" {
		t.Errorf("java 11: %+v", c["java"])
	}
}

func TestHealthPortInUse(t *testing.T) {
	busy := goodTools
	busy.lsof = "COMMAND PID USER FD TYPE\njava 99123 qa 12u IPv4 0t0 TCP 127.0.0.1:18080 (LISTEN)"
	c, overall := checks(t, healthServer(t, busy, goodCharles()))
	if c["port"].Status != "fail" || !strings.Contains(c["port"].Detail, "java (pid 99123)") || overall != "fail" {
		t.Errorf("port: %+v overall %s", c["port"], overall)
	}
}

func TestHealthCharlesAndProxy(t *testing.T) {
	off := goodTools
	off.pgrepErr = errors.New("exit 1")
	off.scutil = "<dictionary> {\n  HTTPEnable : 0\n}"
	c, overall := checks(t, healthServer(t, off, goodCharles()))
	if c["charles"].Status != "warn" || c["macproxy"].Status != "warn" || overall != "warn" {
		t.Errorf("charles %+v macproxy %+v overall %s", c["charles"], c["macproxy"], overall)
	}

	noRule := strings.Replace(strings.Replace(charlesConfig, "%s", "true", 1), "%s", rule("elsewhere.example", "*", "127.0.0.1", "18080", true), 1)
	c, overall = checks(t, healthServer(t, goodTools, noRule))
	if c["rules"].Status != "fail" || !strings.Contains(c["rules"].Detail, "api.example.test") || overall != "fail" {
		t.Errorf("rules: %+v overall %s", c["rules"], overall)
	}

	apiOnly := strings.Replace(strings.Replace(charlesConfig, "%s", "true", 1), "%s", rule("api.example.test", "443", "127.0.0.1", "18080", true), 1)
	if c, _ := checks(t, healthServer(t, goodTools, apiOnly)); c["rules"].Status != "warn" || !strings.Contains(c["rules"].Detail, "Remote Config") {
		t.Errorf("firebase missing: %+v", c["rules"])
	}

	if c, _ := checks(t, healthServer(t, goodTools, "")); c["rules"].Status != "info" {
		t.Errorf("unreadable Charles settings should not fail: %+v", c["rules"])
	}
}

func TestHealthWithNoProject(t *testing.T) {
	s := emptyServer(t, &execx.Fake{}, nil)
	c, overall := checks(t, s)
	if len(c) != 1 || c["project"].Status != "fail" || overall != "fail" {
		t.Fatalf("checks = %+v overall %s", c, overall)
	}
}
