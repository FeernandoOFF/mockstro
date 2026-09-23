package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/fsx"
	"github.com/feernandooff/trun/internal/ojson"
	"github.com/feernandooff/trun/internal/project"
	"github.com/feernandooff/trun/internal/registry"
)

type Android struct {
	P    *project.Project
	Exec execx.Runner

	mu    sync.Mutex
	names map[string]string
}

func (a *Android) adb(ctx context.Context, serial string, args ...string) (string, error) {
	if serial != "" {
		args = append([]string{"-s", serial}, args...)
	}
	return a.Exec.Output(ctx, execx.Cmd{Name: "adb", Args: args})
}

func (a *Android) Devices(ctx context.Context) ([]Device, error) {
	out, err := a.Exec.Output(ctx, execx.Cmd{Name: "adb", Args: []string{"devices"}})
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "device" {
			devs = append(devs, Device{Serial: f[0], Class: a.Class(f[0]), State: "device"})
		}
	}
	return devs, nil
}

func (a *Android) Class(serial string) string {
	if strings.HasPrefix(serial, "emulator-") {
		return "emulator"
	}
	return "physical"
}

// DeviceName is the AVD name behind an emulator serial.
func (a *Android) DeviceName(ctx context.Context, serial string) string {
	a.mu.Lock()
	if n, ok := a.names[serial]; ok {
		a.mu.Unlock()
		return n
	}
	a.mu.Unlock()
	if a.Class(serial) != "emulator" {
		return ""
	}
	name, _ := a.adb(ctx, serial, "shell", "getprop", "ro.boot.qemu.avd_name")
	if name == "" {
		out, _ := a.adb(ctx, serial, "emu", "avd", "name")
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(l); l != "" && l != "OK" {
				name = l
				break
			}
		}
	}
	if name != "" {
		a.mu.Lock()
		a.names[serial] = name
		a.mu.Unlock()
	}
	return name
}

// Designated: .maestro/agent-avds.txt, MAESTRO_AGENT_AVDS, and the global designated list.
func (a *Android) Designated() []string {
	var names []string
	names = append(names, readNameList(a.P.AgentAVDsFile())...)
	names = append(names, splitNames(a.P.Env["MAESTRO_AGENT_AVDS"])...)
	names = append(names, globalDesignated()...)
	return uniq(names)
}

func (a *Android) emulatorBin() string {
	for _, c := range []string{
		filepath.Join(a.P.Env["ANDROID_HOME"], "emulator", "emulator"),
		filepath.Join(a.P.Env["ANDROID_SDK_ROOT"], "emulator", "emulator"),
		filepath.Join(os.Getenv("HOME"), "Library", "Android", "sdk", "emulator", "emulator"),
	} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return c
		}
	}
	if p, err := a.Exec.LookPath("emulator"); err == nil {
		return p
	}
	return ""
}

// InstalledAVDs lists every AVD on this machine (designated or not).
func (a *Android) InstalledAVDs(ctx context.Context) []string {
	bin := a.emulatorBin()
	if bin == "" {
		return nil
	}
	out, _ := a.Exec.Output(ctx, execx.Cmd{Name: bin, Args: []string{"-list-avds"}})
	var avds []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "INFO") {
			avds = append(avds, l)
		}
	}
	return avds
}

func (a *Android) Bootable(ctx context.Context) []string {
	des := a.Designated()
	var out []string
	for _, avd := range a.InstalledAVDs(ctx) {
		if slices.Contains(des, avd) {
			out = append(out, avd)
		}
	}
	return out
}

// Boot starts an AVD and waits until the package manager is ready, not merely until adb sees it.
func (a *Android) Boot(ctx context.Context, avd string, progress func(string)) (string, error) {
	bin := a.emulatorBin()
	if bin == "" {
		return "", fmt.Errorf("android emulator binary not found; set ANDROID_HOME")
	}
	before := map[string]bool{}
	if devs, err := a.Devices(ctx); err == nil {
		for _, d := range devs {
			before[d.Serial] = true
		}
	}
	progress(fmt.Sprintf("Booting %s (a cold boot can take a couple of minutes)...", avd))
	cmd, err := execx.Start(execx.Cmd{Name: bin, Args: []string{"-avd", avd, "-no-snapshot-save"}}, true)
	if err != nil {
		return "", fmt.Errorf("start emulator: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	start := time.Now()
	for i := 0; time.Since(start) < 300*time.Second; i++ {
		if i > 0 && i%15 == 0 {
			progress(fmt.Sprintf("  still waiting for %s (%ds)...", avd, int(time.Since(start).Seconds())))
		}
		devs, _ := a.Devices(ctx)
		for _, d := range devs {
			if before[d.Serial] {
				continue
			}
			a.mu.Lock()
			delete(a.names, d.Serial)
			a.mu.Unlock()
			if a.DeviceName(ctx, d.Serial) != avd {
				continue
			}
			if done, _ := a.adb(ctx, d.Serial, "shell", "getprop", "sys.boot_completed"); done == "1" {
				progress(fmt.Sprintf("Booted %s as %s", avd, d.Serial))
				return d.Serial, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return "", fmt.Errorf("timed out after 300s waiting for %s to finish booting", avd)
}

func (a *Android) EnsureBooted(context.Context, string, func(string)) error { return nil }

// Build runs the incremental gradle task and snapshots the single matching APK into the run.
func (a *Android) Build(ctx context.Context, artifactDir string, out io.Writer) (string, string, error) {
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintf(out, "Building %s (incremental)...\n", a.P.GradleTask)
	if err := a.Exec.Run(ctx, execx.Cmd{Name: "./gradlew", Args: []string{a.P.GradleTask, "-q"}, Dir: a.P.Root, Stdout: out, Stderr: out}); err != nil {
		return "", "", fmt.Errorf("gradle build failed: %s: %w", a.P.GradleTask, err)
	}
	matches, _ := filepath.Glob(filepath.Join(a.P.Root, a.P.APKGlob))
	var built []string
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && !st.IsDir() {
			built = append(built, m)
		}
	}
	if len(built) == 0 {
		return "", "", fmt.Errorf("no APK produced at %s", filepath.Join(a.P.Root, a.P.APKGlob))
	}
	if len(built) > 1 {
		return "", "", fmt.Errorf("multiple APKs match %s; refusing an ambiguous artifact", a.P.APKGlob)
	}
	dir := filepath.Join(artifactDir, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	dst := filepath.Join(dir, "app.apk")
	if err := fsx.CopyAtomic(built[0], dst); err != nil {
		return "", "", fmt.Errorf("could not snapshot built APK: %w", err)
	}
	sha, err := fsx.SHA256(dst)
	if err != nil {
		return "", "", err
	}
	_ = os.WriteFile(filepath.Join(artifactDir, "apk.sha256"), []byte(sha+"  "+dst+"\n"), 0o644)
	return dst, sha, nil
}
func (a *Android) ArtifactSHA(path string) (string, error) { return fsx.SHA256(path) }

func (a *Android) ArtifactExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func (a *Android) Install(ctx context.Context, serial, path string) error {
	// -t is required for envtest builds.
	if _, err := a.adb(ctx, serial, "install", "-r", "-t", path); err != nil {
		return fmt.Errorf("could not install %s on %s: %w", path, serial, err)
	}
	return nil
}

// AfterInstall grants WRITE_SECURE_SETTINGS so E2eConfig can publish its acceptance signal;
// a development permission does not survive a reinstall.
func (a *Android) AfterInstall(ctx context.Context, serial string, overrideActive bool) error {
	if !overrideActive {
		return nil
	}
	if _, err := a.adb(ctx, serial, "shell", "pm", "grant", a.P.AppID, "android.permission.WRITE_SECURE_SETTINGS"); err != nil {
		return fmt.Errorf("could not grant WRITE_SECURE_SETTINGS to %s on %s: %w", a.P.AppID, serial, err)
	}
	return nil
}

var (
	reVersionName = regexp.MustCompile(`versionName=(\S+)`)
	reVersionCode = regexp.MustCompile(`versionCode=(\d+)`)
	reLastUpdate  = regexp.MustCompile(`lastUpdateTime=(.*)`)
)

func (a *Android) InstalledProvenance(ctx context.Context, serial string) (*registry.APK, error) {
	dump, _ := a.adb(ctx, serial, "shell", "dumpsys", "package", a.P.AppID)
	if strings.TrimSpace(dump) == "" || !strings.Contains(dump, "versionCode=") {
		return nil, fmt.Errorf("%s is not installed on %s; drop --skip-build", a.P.AppID, serial)
	}
	apk := &registry.APK{Source: "preinstalled"}
	if m := reVersionName.FindStringSubmatch(dump); m != nil {
		apk.VersionName = m[1]
	}
	if m := reVersionCode.FindStringSubmatch(dump); m != nil {
		apk.VersionCode = m[1]
	}
	if m := reLastUpdate.FindStringSubmatch(dump); m != nil {
		apk.InstalledAt = strings.TrimSpace(m[1])
	}
	return apk, nil
}

// Proxy settings Proxyman/Charles (or `adb shell settings put global http_proxy`) leave behind.
var proxyKeys = []string{"http_proxy", "global_http_proxy_host", "global_http_proxy_port", "global_http_proxy_exclusion_list", "global_proxy_pac_url"}

func emptySetting(v string) bool { return v == "" || v == "null" || v == ":0" || v == "0" }

// DeviceProxy reports a device-wide proxy, e.g. "http_proxy=192.168.1.20:9090".
func (a *Android) DeviceProxy(ctx context.Context, serial string) string {
	var set []string
	for _, k := range proxyKeys {
		v, err := a.adb(ctx, serial, "shell", "settings", "get", "global", k)
		if err == nil && !emptySetting(strings.TrimSpace(v)) {
			set = append(set, k+"="+strings.TrimSpace(v))
		}
	}
	return strings.Join(set, " ")
}

// ClearProxy removes every device-wide proxy setting, then proves it by reading them back.
func (a *Android) ClearProxy(ctx context.Context, serial string) error {
	if _, err := a.adb(ctx, serial, "shell", "settings", "put", "global", "http_proxy", ":0"); err != nil {
		return fmt.Errorf("could not clear http_proxy on %s: %w", serial, err)
	}
	for _, k := range proxyKeys[1:] {
		_, _ = a.adb(ctx, serial, "shell", "settings", "delete", "global", k)
	}
	if left := a.DeviceProxy(ctx, serial); left != "" {
		return fmt.Errorf("proxy still set on %s after clearing: %s", serial, left)
	}
	return nil
}

// AssertNoProxy refuses a device-wide proxy: it silently beats adb reverse. The runner did
// not set it, so it will not remove it.
func (a *Android) AssertNoProxy(ctx context.Context, serial string) error {
	proxy := a.DeviceProxy(ctx, serial)
	if proxy == "" {
		return nil
	}
	return fmt.Errorf(`%s has a device-wide proxy set: %s
  Traffic would go there instead of this run's mock, and flows would fail in the UI with no
  visible cause. Clear it with `+"`trun proxy --clear --device %s`"+`, re-run with --clear-proxy,
  or press p on the device in the TUI dashboard`, serial, proxy, serial)
}

// Payload is the JSON E2eConfig accepts: schema 1, this app id, unexpired, loopback /api.
func (a *Android) Payload(o Override) ([]byte, error) {
	return ojson.Marshal(struct {
		Schema        string            `json:"schema"`
		ApplicationID string            `json:"applicationId"`
		RunID         string            `json:"runId"`
		ExpiresAt     string            `json:"expiresAt"`
		APIURL        string            `json:"apiUrl"`
		RemoteConfig  map[string]string `json:"remoteConfig"`
	}{"1", a.P.AppID, o.RunID, strconv.FormatInt(o.ExpiresAt, 10), o.APIURL(), nonNil(o.FBRC)})
}

// nonNil: E2eConfig expects remoteConfig to be an object, never null.
func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (a *Android) WriteOverride(ctx context.Context, serial string, o Override) error {
	payload, err := a.Payload(o)
	if err != nil {
		return err
	}
	if len(payload) >= 20000 {
		return fmt.Errorf("override payload is too large for Settings.Global (%d bytes)", len(payload))
	}
	// Base64: the value crosses the host and device shells, and Remote Config copy contains
	// apostrophes and ampersands.
	encoded := base64.StdEncoding.EncodeToString(payload)
	// A stale acceptance from an earlier run would otherwise read as this run's proof.
	_, _ = a.adb(ctx, serial, "shell", "settings delete global "+a.P.AcceptKey)
	if _, err := a.adb(ctx, serial, "shell", "settings put global "+a.P.OverrideKey+" "+encoded); err != nil {
		return fmt.Errorf("could not write the device override on %s: %w", serial, err)
	}
	return nil
}

func (a *Android) readOverride(ctx context.Context, serial string) map[string]any {
	enc, _ := a.adb(ctx, serial, "shell", "settings get global "+a.P.OverrideKey)
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// PreflightOverride proves the chain before any flow runs.
func (a *Android) PreflightOverride(ctx context.Context, serial string, o Override) (string, error) {
	if err := a.AssertNoProxy(ctx, serial); err != nil {
		return "", err
	}
	m := a.readOverride(ctx, serial)
	if m == nil || m["runId"] != o.RunID || m["apiUrl"] != o.APIURL() {
		return "", fmt.Errorf("override did not round-trip with this run identity on %s", serial)
	}
	list, _ := a.adb(ctx, serial, "reverse", "--list")
	if !strings.Contains(list, "tcp:"+strconv.Itoa(o.Port)) {
		return "", fmt.Errorf("adb reverse tcp:%d is not active on %s", o.Port, serial)
	}
	return fmt.Sprintf("Device interception active: %s -> 127.0.0.1:%d (runtime override)", serial, o.Port), nil
}

func (a *Android) AcceptedRunID(ctx context.Context, serial string) string {
	out, _ := a.adb(ctx, serial, "shell", "settings get global "+a.P.AcceptKey)
	return strings.TrimSpace(out)
}

func (a *Android) ResetApp(ctx context.Context, serial, runID string) (bool, string) {
	m := a.readOverride(ctx, serial)
	if m == nil || m["runId"] != runID || m["applicationId"] != a.P.AppID {
		return false, fmt.Sprintf("Override on %s is not owned by run %s; leaving app state and reverse untouched.", serial, runID)
	}
	if _, err := a.adb(ctx, serial, "shell", "am", "force-stop", a.P.AppID); err != nil {
		return false, ""
	}
	_, _ = a.adb(ctx, serial, "shell", "settings delete global "+a.P.OverrideKey)
	_, _ = a.adb(ctx, serial, "shell", "settings delete global "+a.P.AcceptKey)
	return true, ""
}

func (a *Android) AttachPort(ctx context.Context, serial string, port int) error {
	p := "tcp:" + strconv.Itoa(port)
	if _, err := a.adb(ctx, serial, "reverse", p, p); err != nil {
		return fmt.Errorf("could not adb reverse %s on %s: %w", p, serial, err)
	}
	return nil
}

// DetachPort removes only this run's mapping, never --remove-all.
func (a *Android) DetachPort(ctx context.Context, serial string, port int) error {
	_, err := a.adb(ctx, serial, "reverse", "--remove", "tcp:"+strconv.Itoa(port))
	return err
}

// IsEmulator also recognises emulators whose serial is not emulator-NNNN.
func (a *Android) IsEmulator(ctx context.Context, serial string) bool {
	if a.Class(serial) == "emulator" {
		return true
	}
	out, _ := a.adb(ctx, serial, "shell", "getprop", "ro.boot.qemu")
	return out == "1"
}

const imeWithHardKeyboard = "show_ime_with_hard_keyboard"

// hasHardwareKeyboard: the setting only hides the on-screen keyboard while one is attached
// (AVD hw.keyboard=yes).
func (a *Android) hasHardwareKeyboard(ctx context.Context, serial string) bool {
	out, _ := a.adb(ctx, serial, "shell", "dumpsys", "input")
	return strings.Contains(out, "KeyboardType: Alphabetic") || strings.Contains(out, "KeyboardType: 2")
}

func (a *Android) Keyboard(ctx context.Context, serial string) string {
	if !a.hasHardwareKeyboard(ctx, serial) {
		return "always"
	}
	v, _ := a.adb(ctx, serial, "shell", "settings", "get", "secure", imeWithHardKeyboard)
	if strings.TrimSpace(v) == "0" {
		return "hidden"
	}
	return "shown"
}

func (a *Android) SetKeyboard(ctx context.Context, serial string, show bool) error {
	val := "0"
	if show {
		val = "1"
	}
	if _, err := a.adb(ctx, serial, "shell", "settings", "put", "secure", imeWithHardKeyboard, val); err != nil {
		return fmt.Errorf("could not set the on-screen keyboard on %s: %w", serial, err)
	}
	if !show && !a.hasHardwareKeyboard(ctx, serial) {
		return fmt.Errorf("%s has no hardware keyboard (AVD hw.keyboard=no), so Android always shows the on-screen keyboard; enable hw.keyboard in the AVD to hide it", serial)
	}
	return nil
}
