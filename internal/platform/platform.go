// Package platform isolates what genuinely differs between Android and iOS: device discovery,
// booting, build/install, and how the app is told which mock to talk to.
//
//	Android  base64 JSON in Settings.Global (read before the HTTP clients exist) + `adb reverse`.
//	iOS      e2e-config.json inside the installed bundle + simulator launchctl environment;
//	         a simulator shares the host loopback, so nothing is forwarded.
//
// Both carry apiUrl, runId, remoteConfig and expiresAt, and both make the app publish the run
// id it honoured so a run proves acceptance instead of assuming it.
package platform

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/paths"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/registry"
)

type Device struct {
	Serial string `json:"serial"`
	Name   string `json:"name,omitempty"`
	Class  string `json:"class"` // emulator | physical
	State  string `json:"state,omitempty"`
}

// Override is what the app reads to point itself at a run's WireMock.
type Override struct {
	RunID     string
	Port      int
	FBRC      map[string]string
	ExpiresAt int64 // unix millis
}

func (o Override) APIURL() string { return "http://127.0.0.1:" + strconv.Itoa(o.Port) + "/api" }

type Platform interface {
	// Devices lists devices the runner can see: ready adb devices, or available simulators.
	Devices(ctx context.Context) ([]Device, error)
	DeviceName(ctx context.Context, serial string) string
	Class(serial string) string
	Designated() []string
	// Bootable lists designated AVDs/simulators installed on this machine.
	Bootable(ctx context.Context) []string
	// Boot starts name; headless skips the emulator window (Android) / Simulator.app (iOS).
	Boot(ctx context.Context, name string, headless bool, progress func(string)) (string, error)
	EnsureBooted(ctx context.Context, serial string, progress func(string)) error

	Build(ctx context.Context, artifactDir string, out io.Writer) (path, sha string, err error)
	ArtifactSHA(path string) (string, error)
	ArtifactExists(path string) bool
	Install(ctx context.Context, serial, path string) error
	InstalledProvenance(ctx context.Context, serial string) (*registry.APK, error)
	AfterInstall(ctx context.Context, serial string, overrideActive bool) error

	AssertNoProxy(ctx context.Context, serial string) error
	// DeviceProxy describes a device-wide HTTP proxy ("" when none); ClearProxy removes it
	// and verifies the device reads back clean.
	DeviceProxy(ctx context.Context, serial string) string
	ClearProxy(ctx context.Context, serial string) error
	// Keyboard reports the on-screen keyboard: "shown", "hidden" or "" when unsupported.
	Keyboard(ctx context.Context, serial string) string
	SetKeyboard(ctx context.Context, serial string, show bool) error
	// Shutdown stops an emulator/simulator and waits until it is gone; physical devices refuse.
	Shutdown(ctx context.Context, serial string) error
	WriteOverride(ctx context.Context, serial string, o Override) error
	PreflightOverride(ctx context.Context, serial string, o Override) (string, error)
	AcceptedRunID(ctx context.Context, serial string) string
	// ResetApp clears this run's override; owned=false means someone else's run is configured
	// on the device and nothing was touched.
	ResetApp(ctx context.Context, serial, runID string) (owned bool, msg string)
	AttachPort(ctx context.Context, serial string, port int) error
	DetachPort(ctx context.Context, serial string, port int) error
}

func New(p *project.Project, platform string, ex execx.Runner) Platform {
	if platform == "ios" {
		return &IOS{P: p, Exec: ex}
	}
	return &Android{P: p, Exec: ex, names: map[string]string{}}
}

// readNameList reads one name per line, ignoring blanks and # comments.
func readNameList(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		// Trim only: simulator names contain spaces ("iPhone 16 Pro").
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func splitNames(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func notInstalled(p *project.Project, serial string) error {
	if p.Lite() {
		return fmt.Errorf("%s is not installed on %s; install it yourself (lite integration does not build, see docs/integration.md)", p.AppID, serial)
	}
	return fmt.Errorf("%s is not installed on %s; drop --skip-build", p.AppID, serial)
}

func globalDesignated() []string { return readNameList(paths.DesignatedFile()) }
