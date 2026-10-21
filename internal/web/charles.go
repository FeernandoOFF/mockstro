package web

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Charles keeps its Map Remote rules in its saved settings. The page's health check reads only
// that one section, to tell a tester whether their rules reach the mock.

type charlesLocation struct {
	Protocol string `xml:"protocol"`
	Host     string `xml:"host"`
	Port     string `xml:"port"`
}

type charlesRule struct {
	Source  charlesLocation `xml:"sourceLocation"`
	Dest    charlesLocation `xml:"destLocation"`
	Enabled *bool           `xml:"enabled"`
}

type charlesMapRemote struct {
	ToolEnabled *bool `xml:"toolEnabled"`
	Mappings    struct {
		Rules []charlesRule `xml:"mapMapping"`
	} `xml:"mappings"`
}

func (m charlesMapRemote) toolOn() bool { return m.ToolEnabled == nil || *m.ToolEnabled }

func defaultCharlesConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Preferences", "com.xk72.charles.config")
}

// readMapRemote finds the "Map Remote" entry in a Charles settings file and decodes it.
func readMapRemote(r io.Reader) (charlesMapRemote, error) {
	dec := xml.NewDecoder(r)
	named := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return charlesMapRemote{}, fmt.Errorf("no Map Remote section found")
		}
		if err != nil {
			return charlesMapRemote{}, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "entry":
			named = false
		case "string":
			var v string
			if err := dec.DecodeElement(&v, &se); err != nil {
				return charlesMapRemote{}, err
			}
			if v == "Map Remote" {
				named = true
			}
		case "map":
			if named {
				var m charlesMapRemote
				if err := dec.DecodeElement(&m, &se); err != nil {
					return charlesMapRemote{}, err
				}
				return m, nil
			}
		}
	}
}

func globMatch(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	ok, _ := regexp.MatchString(re, s)
	return ok
}

func (l charlesLocation) matches(scheme, host, port string) bool {
	return (l.Protocol == "" || strings.EqualFold(l.Protocol, scheme)) && globMatch(l.Host, host) && globMatch(l.Port, port)
}

func (l charlesLocation) isMock(port string) bool {
	switch strings.ToLower(l.Host) {
	case "127.0.0.1", "localhost", "::1":
	default:
		return false
	}
	return (l.Protocol == "" || strings.EqualFold(l.Protocol, "http")) && l.Port == port
}

func (l charlesLocation) String() string {
	proto := l.Protocol
	if proto == "" {
		proto = "http"
	}
	if l.Port == "" {
		return proto + "://" + l.Host
	}
	return proto + "://" + l.Host + ":" + l.Port
}

// mapVerdict says what Charles does with requests to rawURL: Charles applies the first enabled
// rule that matches, so an earlier rule sending the host elsewhere wins over a later one.
func mapVerdict(m charlesMapRemote, rawURL string, mockPort string) (status, detail string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "info", "Could not work out the host from " + rawURL
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	if !m.toolOn() {
		return "fail", "Map Remote is switched off in Charles (Tools menu)"
	}
	for _, rule := range m.Mappings.Rules {
		if rule.Enabled != nil && !*rule.Enabled {
			continue
		}
		if !rule.Source.matches(u.Scheme, u.Hostname(), port) {
			continue
		}
		if rule.Dest.isMock(mockPort) {
			return "ok", "A rule sends " + u.Hostname() + " to the mock"
		}
		return "fail", "A rule matches " + u.Hostname() + " but sends it to " + rule.Dest.String() + " instead of the mock"
	}
	return "fail", "No enabled rule sends " + u.Hostname() + " to the mock"
}
