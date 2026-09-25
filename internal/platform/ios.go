package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fsx"
	"github.com/feernandooff/mockstro/internal/ojson"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/registry"
)

var iosEnvKeys = []string{"E2E_API_URL", "E2E_RUN_ID", "E2E_REMOTE_CONFIG", "E2E_EXPIRES_AT"}

type IOS struct {
	P    *project.Project
	Exec execx.Runner

	// BootKeyboard ("", "hidden", "shown") is written before booting, so the run's keyboard
	// choice lands on the first boot instead of costing a second one.
	BootKeyboard string

	mu       sync.Mutex
	cache    []simDevice
	cachedAt time.Time
}

type simDevice struct {
	UDID        string `json:"udid"`
	Name        string `json:"name"`
	State       string `json:"state"`
	IsAvailable bool   `json:"isAvailable"`
}

func (i *IOS) simctl(ctx context.Context, args ...string) (string, error) {
	return i.Exec.Output(ctx, execx.Cmd{Name: "xcrun", Args: append([]string{"simctl"}, args...)})
}

// sims is cached briefly: `simctl list -j` costs about a second.
func (i *IOS) sims(ctx context.Context, fresh bool) []simDevice {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !fresh && i.cache != nil && time.Since(i.cachedAt) < 3*time.Second {
		return i.cache
	}
	out, err := i.simctl(ctx, "list", "devices", "available", "-j")
	if err != nil {
		return i.cache
	}
	var parsed struct {
		Devices map[string][]simDevice `json:"devices"`
	}
	if json.Unmarshal([]byte(out), &parsed) != nil {
		return i.cache
	}
	var all []simDevice
	for _, list := range parsed.Devices {
		for _, d := range list {
			if d.IsAvailable {
				all = append(all, d)
			}
		}
	}
	i.cache, i.cachedAt = all, time.Now()
	return all
}

func (i *IOS) Devices(ctx context.Context) ([]Device, error) {
	var out []Device
	for _, s := range i.sims(ctx, false) {
		out = append(out, Device{Serial: s.UDID, Name: s.Name, Class: "emulator", State: s.State})
	}
	return out, nil
}

func (i *IOS) DeviceName(ctx context.Context, serial string) string {
	for _, s := range i.sims(ctx, false) {
		if s.UDID == serial {
			return s.Name
		}
	}
	return ""
}

// A simulator is the iOS equivalent of an emulator: disposable, never someone's phone.
func (i *IOS) Class(string) string { return "emulator" }

func (i *IOS) Designated() []string {
	return uniq(append(append([]string(nil), i.P.IOSSimNames...), globalDesignated()...))
}

func (i *IOS) Bootable(ctx context.Context) []string {
	des := i.Designated()
	seen := map[string]bool{}
	var out []string
	for _, s := range i.sims(ctx, false) {
		if slices.Contains(des, s.Name) && !seen[s.Name] && s.State != "Booted" {
			seen[s.Name] = true
			out = append(out, s.Name)
		}
	}
	return out
}

func (i *IOS) Boot(ctx context.Context, name string, headless bool, progress func(string)) (string, error) {
	udid := ""
	for _, s := range i.sims(ctx, true) {
		if s.Name == name {
			udid = s.UDID
		}
	}
	if udid == "" {
		return "", fmt.Errorf("no available simulator named %s", name)
	}
	if i.BootKeyboard != "" {
		if err := i.writeKeyboardPref(ctx, udid, i.BootKeyboard == "shown"); err != nil {
			progress(err.Error())
		}
	}
	progress(fmt.Sprintf("Booting simulator %s...", name))
	_, _ = i.simctl(ctx, "boot", udid)
	if _, err := i.simctl(ctx, "bootstatus", udid, "-b"); err != nil {
		return "", fmt.Errorf("simulator %s did not finish booting", name)
	}
	progress(fmt.Sprintf("Booted %s as %s", name, udid))
	if !headless {
		i.showSimulator(ctx, progress)
	}
	i.sims(ctx, true)
	return udid, nil
}

func (i *IOS) EnsureBooted(ctx context.Context, serial string, progress func(string)) error {
	state := ""
	for _, s := range i.sims(ctx, true) {
		if s.UDID == serial {
			state = s.State
		}
	}
	if state != "Booted" {
		progress(fmt.Sprintf("Booting simulator %s...", serial))
		if _, err := i.simctl(ctx, "bootstatus", serial, "-b"); err != nil {
			return fmt.Errorf("simulator %s did not finish booting", serial)
		}
	}
	i.showSimulator(ctx, progress)
	return nil
}

// Simulator.app renders whichever devices are booted, so it only has to be running.
func (i *IOS) showSimulator(ctx context.Context, progress func(string)) {
	if _, err := i.Exec.Output(ctx, execx.Cmd{Name: "open", Args: []string{"-a", "Simulator"}}); err != nil {
		progress("Could not open Simulator.app; continuing headless.")
	}
}

func (i *IOS) Build(ctx context.Context, artifactDir string, out io.Writer) (string, string, error) {
	if i.P.IOSProject == "" || i.P.IOSScheme == "" {
		return "", "", fmt.Errorf("set IOS_PROJECT and IOS_SCHEME in .maestro/project.env")
	}
	if out == nil {
		out = io.Discard
	}
	derived := filepath.Join(i.P.Root, "build", "maestro-derived-data")
	fmt.Fprintf(out, "Building %s (%s, incremental)...\n", i.P.IOSScheme, i.P.IOSConfig)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", "", err
	}
	logPath := filepath.Join(artifactDir, "xcodebuild.log")
	logf, err := os.Create(logPath)
	if err != nil {
		return "", "", err
	}
	projFlag := "-project"
	if strings.HasSuffix(i.P.IOSProject, ".xcworkspace") {
		projFlag = "-workspace"
	}
	err = i.Exec.Run(ctx, execx.Cmd{Name: "xcodebuild", Args: []string{
		projFlag, filepath.Join(i.P.Root, i.P.IOSProject),
		"-scheme", i.P.IOSScheme,
		"-configuration", i.P.IOSConfig,
		"-sdk", "iphonesimulator",
		"-destination", "generic/platform=iOS Simulator",
		"-derivedDataPath", derived,
		"CODE_SIGNING_ALLOWED=NO",
		"build",
	}, Stdout: logf, Stderr: logf})
	logf.Close()
	if err != nil {
		return "", "", fmt.Errorf("xcodebuild failed; see %s", i.P.Rel(logPath))
	}
	matches, _ := filepath.Glob(filepath.Join(derived, "Build", "Products", "*", "*.app"))
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no .app produced under %s", derived)
	}
	app := matches[0]
	sha, _ := i.ArtifactSHA(app)
	_ = os.WriteFile(filepath.Join(artifactDir, "apk.sha256"), []byte(sha+"  "+app+"\n"), 0o644)
	return app, sha, nil
}

// A bundle is a directory, so hash its executable.
func (i *IOS) ArtifactSHA(path string) (string, error) {
	exe := filepath.Join(path, strings.TrimSuffix(filepath.Base(path), ".app"))
	sum, err := fsx.SHA256(exe)
	if err != nil {
		return "unhashed", nil
	}
	return sum, nil
}

func (i *IOS) ArtifactExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func (i *IOS) Install(ctx context.Context, serial, path string) error {
	if _, err := i.simctl(ctx, "install", serial, path); err != nil {
		return fmt.Errorf("could not install %s on %s: %w", path, serial, err)
	}
	return nil
}

func (i *IOS) container(ctx context.Context, serial, kind string) string {
	out, err := i.simctl(ctx, "get_app_container", serial, i.P.AppID, kind)
	if err != nil {
		return ""
	}
	if st, err := os.Stat(out); err == nil && st.IsDir() {
		return out
	}
	return ""
}

func (i *IOS) InstalledProvenance(ctx context.Context, serial string) (*registry.APK, error) {
	app := i.container(ctx, serial, "app")
	if app == "" {
		return nil, notInstalled(i.P, serial)
	}
	plist := filepath.Join(app, "Info.plist")
	read := func(key string) string {
		out, _ := i.Exec.Output(ctx, execx.Cmd{Name: "/usr/libexec/PlistBuddy", Args: []string{"-c", "Print :" + key, plist}})
		return out
	}
	apk := &registry.APK{Source: "preinstalled", VersionName: read("CFBundleShortVersionString"), VersionCode: read("CFBundleVersion")}
	if st, err := os.Stat(app); err == nil {
		apk.InstalledAt = st.ModTime().Format(time.RFC3339)
	}
	return apk, nil
}

func (i *IOS) AssertNoProxy(context.Context, string) error { return nil }

// A simulator uses the Mac's network settings; there is no device-level proxy to manage.
func (i *IOS) DeviceProxy(context.Context, string) string { return "" }
func (i *IOS) ClearProxy(context.Context, string) error   { return nil }

// The on-screen keyboard follows the Simulator's per-device "Connect Hardware Keyboard"
// preference: connected (YES, the Simulator default) hides it.
func simulatorPlist() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "Preferences", "com.apple.iphonesimulator.plist")
}

func (i *IOS) Keyboard(ctx context.Context, serial string) string {
	out, err := i.Exec.Output(ctx, execx.Cmd{Name: "plutil", Args: []string{"-extract", "DevicePreferences." + serial + ".ConnectHardwareKeyboard", "raw", simulatorPlist()}})
	if err == nil && strings.TrimSpace(out) == "false" {
		return "shown"
	}
	return "hidden"
}

// SetKeyboard writes the preference and reboots a booted simulator so it takes effect.
func (i *IOS) SetKeyboard(ctx context.Context, serial string, show bool) error {
	if (i.Keyboard(ctx, serial) == "shown") == show {
		return nil
	}
	if err := i.writeKeyboardPref(ctx, serial, show); err != nil {
		return err
	}
	for _, s := range i.sims(ctx, true) {
		if s.UDID == serial && s.State == "Booted" {
			_, _ = i.simctl(ctx, "shutdown", serial)
			_, _ = i.simctl(ctx, "boot", serial)
			if _, err := i.simctl(ctx, "bootstatus", serial, "-b"); err != nil {
				return fmt.Errorf("simulator %s did not come back after the keyboard change", serial)
			}
			i.sims(ctx, true)
		}
	}
	return nil
}

// writeKeyboardPref records the per-simulator preference; it takes effect on the next boot.
func (i *IOS) writeKeyboardPref(ctx context.Context, serial string, show bool) error {
	plist := simulatorPlist()
	plutil := func(args ...string) error {
		_, err := i.Exec.Output(ctx, execx.Cmd{Name: "plutil", Args: append(args, plist)})
		return err
	}
	// Parents may not exist yet; inserting an existing key fails harmlessly.
	_ = plutil("-insert", "DevicePreferences", "-dictionary")
	_ = plutil("-insert", "DevicePreferences."+serial, "-dictionary")
	connect := "YES"
	if show {
		connect = "NO"
	}
	if err := plutil("-replace", "DevicePreferences."+serial+".ConnectHardwareKeyboard", "-bool", connect); err != nil {
		return fmt.Errorf("could not set the keyboard preference for %s: %w", serial, err)
	}
	return nil
}

func fbrcJSON(m map[string]string) string {
	b, _ := ojson.Marshal(nonNil(m))
	return string(b)
}

// WriteOverride puts the config into the installed bundle (what the app reads on launch) and
// the simulator environment (what a manual `simctl launch` inherits). Neither is touched by
// clearState, which lives in the data container.
func (i *IOS) WriteOverride(ctx context.Context, serial string, o Override) error {
	bundle := i.container(ctx, serial, "app")
	if bundle == "" {
		return fmt.Errorf("could not locate the installed app on %s", serial)
	}
	fbrc := fbrcJSON(o.FBRC)
	exp := strconv.FormatInt(o.ExpiresAt, 10)
	b, _ := ojson.Marshal(struct {
		APIURL       string `json:"e2eApiUrl"`
		RunID        string `json:"e2eRunId"`
		RemoteConfig string `json:"e2eRemoteConfig"`
		ExpiresAt    string `json:"e2eExpiresAt"`
	}{o.APIURL(), o.RunID, fbrc, exp})
	if err := os.WriteFile(filepath.Join(bundle, "e2e-config.json"), b, 0o644); err != nil {
		return fmt.Errorf("could not write the override into %s: %w", bundle, err)
	}
	for k, v := range map[string]string{"E2E_API_URL": o.APIURL(), "E2E_RUN_ID": o.RunID, "E2E_REMOTE_CONFIG": fbrc, "E2E_EXPIRES_AT": exp} {
		if _, err := i.simctl(ctx, "spawn", serial, "launchctl", "setenv", k, v); err != nil {
			return fmt.Errorf("could not set %s on simulator %s: %w", k, serial, err)
		}
	}
	return nil
}

func (i *IOS) PreflightOverride(ctx context.Context, serial string, o Override) (string, error) {
	bundle := i.container(ctx, serial, "app")
	if bundle == "" {
		return "", fmt.Errorf("the app is not installed on %s", serial)
	}
	var cfg map[string]any
	b, _ := os.ReadFile(filepath.Join(bundle, "e2e-config.json"))
	_ = json.Unmarshal(b, &cfg)
	seen, _ := cfg["e2eApiUrl"].(string)
	if seen != o.APIURL() {
		if seen == "" {
			seen = "nothing"
		}
		return "", fmt.Errorf("the app bundle on %s does not carry this run's endpoint (got '%s')", serial, seen)
	}
	return fmt.Sprintf("Simulator interception active: %s -> 127.0.0.1:%d (simulator environment)", serial, o.Port), nil
}

// AcceptedRunID is published by the app into its data container.
func (i *IOS) AcceptedRunID(ctx context.Context, serial string) string {
	data := i.container(ctx, serial, "data")
	if data == "" {
		return ""
	}
	b, _ := os.ReadFile(filepath.Join(data, "Documents", "e2e-accepted-run-id"))
	return strings.TrimSpace(string(b))
}

func (i *IOS) ResetApp(ctx context.Context, serial, runID string) (bool, string) {
	current, _ := i.simctl(ctx, "spawn", serial, "launchctl", "getenv", "E2E_RUN_ID")
	if current != "" && current != runID {
		return false, fmt.Sprintf("Simulator %s is configured for run %s, not %s; leaving it alone.", serial, current, runID)
	}
	_, _ = i.simctl(ctx, "terminate", serial, i.P.AppID)
	for _, k := range iosEnvKeys {
		_, _ = i.simctl(ctx, "spawn", serial, "launchctl", "unsetenv", k)
	}
	if bundle := i.container(ctx, serial, "app"); bundle != "" {
		os.Remove(filepath.Join(bundle, "e2e-config.json"))
	}
	if data := i.container(ctx, serial, "data"); data != "" {
		os.Remove(filepath.Join(data, "Documents", "e2e-accepted-run-id"))
	}
	return true, ""
}

func (i *IOS) AfterInstall(context.Context, string, bool) error { return nil }

func (i *IOS) AttachPort(context.Context, string, int) error { return nil }
func (i *IOS) DetachPort(context.Context, string, int) error { return nil }

func (i *IOS) Shutdown(ctx context.Context, serial string) error {
	if _, err := i.simctl(ctx, "shutdown", serial); err != nil {
		return fmt.Errorf("could not shut down simulator %s: %w", serial, err)
	}
	return nil
}
