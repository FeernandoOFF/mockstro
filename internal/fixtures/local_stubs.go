package fixtures

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// LocalStubDir holds stubs saved from the web UI. They sit beside the team's stubs but are not
// listed in any profile, so they are served only when asked for (the UI's Apply, or an extra
// `mockstro mock <target> local/<name>` argument) until a developer promotes them.
const LocalStubDir = "local"

var (
	localName   = regexp.MustCompile(`^local/[a-z0-9]+(-[a-z0-9]+)*$`)
	slugRunes   = regexp.MustCompile(`[^a-z0-9]+`)
	maxSlugLen  = 60
	maxTitleLen = 80
)

// LocalStub is one UI-made stub, in the plain terms the UI deals in.
type LocalStub struct {
	Name    string            `json:"name"` // local/<slug>, the stub name the rest of mockstro uses
	Title   string            `json:"title"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	DelayMs int               `json:"delayMs"`
}

type localStubFile struct {
	Name     string `json:"name"`
	Metadata struct {
		Mockstro struct {
			Title  string `json:"title"`
			Source string `json:"source"`
		} `json:"mockstro"`
	} `json:"metadata"`
	Request struct {
		Method  string `json:"method"`
		URLPath string `json:"urlPath"`
	} `json:"request"`
	Response struct {
		Status                 int               `json:"status"`
		Headers                map[string]string `json:"headers,omitempty"`
		JSONBody               json.RawMessage   `json:"jsonBody,omitempty"`
		Body                   string            `json:"body,omitempty"`
		BodyFileName           string            `json:"bodyFileName,omitempty"`
		FixedDelayMilliseconds int               `json:"fixedDelayMilliseconds,omitempty"`
	} `json:"response"`
}

func (s *Store) localRoot() string { return filepath.Join(s.P.StubsRoot(), LocalStubDir) }

func slugify(title string) string {
	slug := strings.Trim(slugRunes.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > maxSlugLen {
		slug = strings.Trim(slug[:maxSlugLen], "-")
	}
	return slug
}

func isJSONContentType(headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, "Content-Type") {
			return strings.Contains(strings.ToLower(v), "json")
		}
	}
	return true
}

// SaveLocalStub writes the stub as stubs/local/<slug>.json and returns it with its Name set. A
// title that is already taken gets a numeric suffix, so nothing is ever overwritten. The file is
// checked with the same rules as every other stub and removed again if it fails them.
func (s *Store) SaveLocalStub(in LocalStub) (LocalStub, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || len(title) > maxTitleLen {
		return in, fmt.Errorf("give the response a name of up to %d characters", maxTitleLen)
	}
	slug := slugify(title)
	if slug == "" {
		return in, fmt.Errorf("the name needs at least one letter or number")
	}

	var f localStubFile
	f.Metadata.Mockstro.Title, f.Metadata.Mockstro.Source = title, "ui"
	f.Request.Method, f.Request.URLPath = in.Method, in.Path
	f.Response.Status, f.Response.Headers, f.Response.FixedDelayMilliseconds = in.Status, in.Headers, in.DelayMs
	if strings.TrimSpace(in.Body) != "" {
		if json.Valid([]byte(in.Body)) && isJSONContentType(in.Headers) {
			f.Response.JSONBody = json.RawMessage(in.Body)
		} else {
			f.Response.Body = in.Body
		}
	}

	if err := os.MkdirAll(s.localRoot(), 0o755); err != nil {
		return in, err
	}
	var file *os.File
	var name string
	for n := 1; n < 100; n++ {
		candidate := slug
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", slug, n)
		}
		name = LocalStubDir + "/" + candidate
		var err error
		file, err = os.OpenFile(s.StubPath(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return in, err
		}
		file = nil
	}
	if file == nil {
		return in, fmt.Errorf("too many saved responses called %q; pick another name", title)
	}
	f.Name = name

	body, err := marshalStub(&f)
	if err == nil {
		_, err = file.Write(body)
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = s.checkSaved(name)
	}
	if err != nil {
		_ = os.Remove(s.StubPath(name))
		return in, err
	}
	in.Name, in.Title = name, title
	return in, nil
}

func marshalStub(f *localStubFile) ([]byte, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func (s *Store) checkSaved(name string) error {
	resolved, err := s.ResolvedStub(name)
	if err != nil {
		return err
	}
	return s.ValidateStub(resolved, name, s.StubPath(name))
}

// LocalStubs lists the saved stubs the UI can show and apply, newest name last. Stubs a developer
// dropped into local/ by hand that use other matchers or body files are skipped here; they still
// work from the command line.
func (s *Store) LocalStubs() ([]LocalStub, error) {
	entries, err := os.ReadDir(s.localRoot())
	if errors.Is(err, os.ErrNotExist) {
		return []LocalStub{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []LocalStub{}
	for _, e := range entries {
		base, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		name := LocalStubDir + "/" + base
		if !localName.MatchString(name) {
			continue
		}
		b, err := os.ReadFile(s.StubPath(name))
		if err != nil {
			continue
		}
		var f localStubFile
		if json.Unmarshal(b, &f) != nil || f.Request.URLPath == "" || f.Request.Method == "" || f.Response.BodyFileName != "" {
			continue
		}
		stub := LocalStub{Name: name, Title: f.Metadata.Mockstro.Title, Method: f.Request.Method, Path: f.Request.URLPath,
			Status: f.Response.Status, Headers: f.Response.Headers, DelayMs: f.Response.FixedDelayMilliseconds}
		if stub.Title == "" {
			stub.Title = base
		}
		if stub.Headers == nil {
			stub.Headers = map[string]string{}
		}
		switch {
		case len(f.Response.JSONBody) > 0:
			var pretty bytes.Buffer
			if json.Indent(&pretty, f.Response.JSONBody, "", "  ") == nil {
				stub.Body = pretty.String()
			}
		default:
			stub.Body = f.Response.Body
		}
		out = append(out, stub)
	}
	return out, nil
}

// LocalStub reads one saved stub by name.
func (s *Store) LocalStub(name string) (LocalStub, error) {
	if !localName.MatchString(name) {
		return LocalStub{}, fmt.Errorf("%q is not a saved response", name)
	}
	all, err := s.LocalStubs()
	if err != nil {
		return LocalStub{}, err
	}
	for _, st := range all {
		if st.Name == name {
			return st, nil
		}
	}
	return LocalStub{}, fmt.Errorf("no saved response %q", name)
}

// DeleteLocalStub removes a saved stub; only names under local/ are ever touched.
func (s *Store) DeleteLocalStub(name string) error {
	if !localName.MatchString(name) {
		return fmt.Errorf("%q is not a saved response", name)
	}
	if err := os.Remove(s.StubPath(name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no saved response %q", name)
		}
		return err
	}
	return nil
}
