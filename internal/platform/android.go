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

	"github.com/feernandooff/mockstro/internal/build"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fsx"
	"github.com/feernandooff/mockstro/internal/ojson"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/registry"
)

type Android struct {
	P    *project.Project
	Exec execx.Runner
	// Cores / MemoryMB override the AVD's hw.cpu.ncore / hw.ramSize for emulators mockstro boots.
	Cores, MemoryMB int
	// GPU is passed as -gpu (e.g. "host"). Headless defaults to SwiftShader, which renders on
	// the CPU; "host" keeps rendering on the Mac's GPU.
	GPU string
	// Snapshot boots from a named emulator snapshot (seconds instead of a cold boot); with
	// -no-snapshot-save it is never overwritten, so every boot starts from the same state.
	Snapshot string
	// settleTick is how often settle samples CPU use (tests shorten it).
	settleTick time.Duration

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
		// Tab-separated: wireless-debugging serials contain spaces ("adb-X (2)._adb-tls-connect._tcp").
		serial, state, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && strings.TrimSpace(state) == "device" {
			devs = append(devs, Device{Serial: serial, Class: a.Class(serial), State: "device"})
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

// DeviceName is the AVD name behind an emulator serial, or a phone's model.
func (a *Android) DeviceName(ctx context.Context, serial string) string {
	a.mu.Lock()
	if n, ok := a.names[serial]; ok {
		a.mu.Unlock()
		return n
	}
	a.mu.Unlock()
	prop := "ro.boot.qemu.avd_name"
	if a.Class(serial) != "emulator" {
		prop = "ro.product.model"
	}
	name, _ := a.adb(ctx, serial, "shell", "getprop", prop)
	if name == "" && a.Class(serial) == "emulator" {
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

// Designated: configured platform devices and the global machine designation list.
func (a *Android) Designated() []string {
	var names []string
	names = append(names, splitNames(a.P.Env["MAESTRO_AGENT_AVDS"])...)
	names = append(names, GlobalDesignated()...)
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
func (a *Android) Boot(ctx context.Context, avd string, headless bool, progress func(string)) (string, error) {
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
	args := []string{"-avd", avd, "-no-snapshot-save"}
	if headless {
		args = append(args, "-no-window", "-no-audio")
		progress(fmt.Sprintf("Booting %s headless (no window, GPU rendering).", avd))
	}
	if a.Snapshot != "" {
		args = append(args, "-snapshot", a.Snapshot)
		progress(fmt.Sprintf("Booting %s from snapshot %s.", avd, a.Snapshot))
	}
	gpu := a.GPU
	if gpu == "" && headless {
		// Headless otherwise falls back to SwiftShader (rendering on the CPU), which saturated
		// the host with 3 emulators; -gpu host keeps it on the Mac's GPU.
		gpu = "host"
	}
	if gpu != "" {
		args = append(args, "-gpu", gpu)
	}
	if a.Cores > 0 {
		args = append(args, "-cores", strconv.Itoa(a.Cores))
	}
	if a.MemoryMB > 0 {
		args = append(args, "-memory", strconv.Itoa(a.MemoryMB))
	}
	if a.Cores > 0 || a.MemoryMB > 0 {
		progress(fmt.Sprintf("Booting %s with cores=%d memory=%dMB (0 = AVD default).", avd, a.Cores, a.MemoryMB))
	}
	cmd, err := execx.Start(execx.Cmd{Name: bin, Args: args}, true)
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
				a.settle(ctx, d.Serial, progress)
				if err := ctx.Err(); err != nil {
					return "", err
				}
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

// settle waits until a freshly booted emulator is mostly idle. Right after boot, app
// compilation and Play services starve the CPU; flows started then hit "isn't responding"
// dialogs from System UI, the launcher or Chrome.
func (a *Android) settle(ctx context.Context, serial string, progress func(string)) {
	const (
		idleNeeded = 0.6
		calmNeeded = 3
		limit      = 3 * time.Minute
	)
	tick := a.settleTick
	if tick == 0 {
		tick = 2 * time.Second
	}
	start := time.Now()
	// Bound adb itself too: a stalled shell must not bypass the settling time limit.
	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	prevBusy, prevTotal, ok := a.cpuTimes(waitCtx, serial)
	if !ok {
		return
	}
	progress(fmt.Sprintf("Waiting for %s to settle after boot...", serial))
	calm := 0
	for time.Since(start) < limit {
		select {
		case <-waitCtx.Done():
			if ctx.Err() == nil {
				progress(fmt.Sprintf("%s still busy after %ds; continuing.", serial, int(limit.Seconds())))
			}
			return
		case <-time.After(tick):
		}
		busy, total, ok := a.cpuTimes(waitCtx, serial)
		if !ok || total <= prevTotal {
			continue
		}
		idle := 1 - float64(busy-prevBusy)/float64(total-prevTotal)
		prevBusy, prevTotal = busy, total
		if idle < idleNeeded {
			calm = 0
			continue
		}
		if calm++; calm >= calmNeeded {
			progress(fmt.Sprintf("%s settled after %ds.", serial, int(time.Since(start).Seconds())))
			return
		}
	}
	progress(fmt.Sprintf("%s still busy after %ds; continuing.", serial, int(limit.Seconds())))
}

// cpuTimes reads the aggregate cpu line of /proc/stat; I/O wait counts as busy.
func (a *Android) cpuTimes(ctx context.Context, serial string) (busy, total uint64, ok bool) {
	out, err := a.adb(ctx, serial, "shell", "head", "-1", "/proc/stat")
	fields := strings.Fields(out)
	if err != nil || len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, f := range fields[1:] {
		// guest and guest_nice are already included in user and nice.
		if i >= 8 {
			break
		}
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total += v
		if i != 3 { // idle
			busy += v
		}
	}
	return busy, total, true
}

func (a *Android) EnsureBooted(context.Context, string, bool, func(string)) error { return nil }

// Build runs the incremental gradle task and snapshots the single matching APK into the run.
func (a *Android) Build(ctx context.Context, artifactDir string, out io.Writer) (string, string, error) {
	return build.Build(ctx, a.P, "android", a.Exec, artifactDir, out)
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

// AfterInstall grants WRITE_SECURE_SETTINGS so the app can publish its acceptance signal;
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
		return nil, notInstalled(a.P, serial)
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
  visible cause. Clear it with `+"`mockstro proxy --clear --device %s`"+`, re-run with --clear-proxy,
  or press p on the device in the TUI dashboard`, serial, proxy, serial)
}

// Payload is the JSON the app accepts: schema 1, this app id, unexpired, loopback /api.
func (a *Android) Payload(o Override) ([]byte, error) {
	return ojson.Marshal(struct {
		Schema        string            `json:"schema"`
		ApplicationID string            `json:"applicationId"`
		RunID         string            `json:"runId"`
		ExpiresAt     string            `json:"expiresAt"`
		APIURL        string            `json:"apiUrl"`
		WebSocketURL  string            `json:"websocketUrl,omitempty"`
		RemoteConfig  map[string]string `json:"remoteConfig"`
	}{"1", a.P.AppID, o.RunID, strconv.FormatInt(o.ExpiresAt, 10), o.APIURL(), o.WebSocketURL(), nonNil(o.FBRC)})
}

// nonNil: the app expects remoteConfig to be an object, never null.
func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (a *Android) StopApp(ctx context.Context, serial string) error {
	_, err := a.adb(ctx, serial, "shell", "am", "force-stop", a.P.AppID)
	return err
}

func (a *Android) WriteOverride(ctx context.Context, serial string, o Override) error {
	if o.WebSocketURL() != "" {
		if _, err := a.adb(ctx, serial, "shell", "settings delete global "+a.P.AcceptKey); err != nil {
			return err
		}
	}
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
	if m == nil || m["runId"] != o.RunID || m["apiUrl"] != o.APIURL() || o.WebSocketURL() != "" && m["websocketUrl"] != o.WebSocketURL() {
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

// The on-screen keyboard is hidden by disabling every keyboard app's package: Android
// re-enables a system keyboard on any install (the app, Maestro's driver) while none is
// enabled, but only from enabled packages. Maestro's own input method is left alone. The
// disabled packages are recorded on the device so "shown" can re-enable exactly those.
const (
	maestroPkg      = "dev.mobile.maestro"
	disabledIMEsKey = "mockstro_disabled_ime_packages"
)

// imePackages lists packages providing an input method (all=false: only enabled ones).
func (a *Android) imePackages(ctx context.Context, serial string, all bool) []string {
	args := []string{"shell", "ime", "list", "-s"}
	if all {
		args = append(args, "-a")
	}
	out, _ := a.adb(ctx, serial, args...)
	var pkgs []string
	for _, id := range strings.Fields(out) {
		pkg, _, _ := strings.Cut(id, "/")
		if pkg != maestroPkg && !slices.Contains(pkgs, pkg) {
			pkgs = append(pkgs, pkg)
		}
	}
	return pkgs
}

func (a *Android) Keyboard(ctx context.Context, serial string) string {
	if len(a.imePackages(ctx, serial, false)) == 0 {
		return "hidden"
	}
	return "shown"
}

func (a *Android) SetKeyboard(ctx context.Context, serial string, show bool) error {
	recorded, _ := a.adb(ctx, serial, "shell", "settings", "get", "global", disabledIMEsKey)
	var prev []string
	if r := strings.TrimSpace(recorded); r != "" && r != "null" {
		prev = strings.Split(r, ",")
	}
	if show {
		for _, pkg := range prev {
			if _, err := a.adb(ctx, serial, "shell", "pm", "enable", pkg); err != nil {
				return fmt.Errorf("could not re-enable %s on %s: %w", pkg, serial, err)
			}
		}
		_, _ = a.adb(ctx, serial, "shell", "settings", "delete", "global", disabledIMEsKey)
		if _, err := a.adb(ctx, serial, "shell", "ime", "reset"); err != nil {
			return fmt.Errorf("could not restore the on-screen keyboard on %s: %w", serial, err)
		}
		return nil
	}
	for _, pkg := range a.imePackages(ctx, serial, true) {
		if _, err := a.adb(ctx, serial, "shell", "pm", "disable-user", "--user", "0", pkg); err != nil {
			return fmt.Errorf("could not disable keyboard app %s on %s: %w", pkg, serial, err)
		}
		if !slices.Contains(prev, pkg) {
			prev = append(prev, pkg)
		}
	}
	if len(prev) > 0 {
		_, _ = a.adb(ctx, serial, "shell", "settings", "put", "global", disabledIMEsKey, strings.Join(prev, ","))
	}
	if left := a.imePackages(ctx, serial, false); len(left) > 0 {
		return fmt.Errorf("%s still has keyboard apps enabled: %s", serial, strings.Join(left, ", "))
	}
	return nil
}

func (a *Android) Shutdown(ctx context.Context, serial string) error {
	if !a.IsEmulator(ctx, serial) {
		return fmt.Errorf("%s is a physical device; not shutting it down", serial)
	}
	if _, err := a.adb(ctx, serial, "emu", "kill"); err != nil {
		return fmt.Errorf("could not stop %s: %w", serial, err)
	}
	for {
		devs, _ := a.Devices(ctx)
		if !slices.ContainsFunc(devs, func(d Device) bool { return d.Serial == serial }) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not shut down: %w", serial, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
