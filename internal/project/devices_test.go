package project

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSetAgentDevicePreservesSharedConfig(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mockstro.yaml", `# shared suite
env: {USER: demo}
platforms:
  android:
    appId: example.android # keep app
    integration: full
    devices:
      - Keep # keep device comment
  ios:
    appId: example.ios
    devices: [iPhone]
`)
	path := filepath.Join(root, ".maestro/mockstro.yaml")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		p, saved, err := SetAgentDevice(root, "android", "New_AVD", true)
		if err != nil || saved != path {
			t.Fatal(saved, err)
		}
		if !slices.Equal(p.Targets["android"].Devices, []string{"Keep", "New_AVD"}) || !slices.Equal(p.Targets["ios"].Devices, []string{"iPhone"}) {
			t.Fatal("designation changed unrelated targets or duplicated a name", p.Targets)
		}
	}
	p, _, err := SetAgentDevice(root, "android", "New_AVD", false)
	if err != nil || !slices.Equal(p.Targets["android"].Devices, []string{"Keep"}) || p.Variables["USER"] != "demo" {
		t.Fatal(p, err)
	}
	b, _ := os.ReadFile(path)
	for _, comment := range []string{"# shared suite", "# keep app", "# keep device comment"} {
		if !strings.Contains(string(b), comment) {
			t.Fatalf("lost %q:\n%s", comment, b)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("changed config permissions to %o", info.Mode().Perm())
	}
}

func TestSetAgentDeviceHonorsLocalOverride(t *testing.T) {
	for _, ownsList := range []bool{true, false} {
		t.Run(map[bool]string{true: "local-devices", false: "local-env-only"}[ownsList], func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, "mockstro.yaml", "platforms: {android: {appId: x, devices: [Shared]}}\n")
			local := "env: {USER: local}\nplatforms:\n  android:\n    project: ../source\n"
			if ownsList {
				local += "    devices: [] # machine devices\n"
			}
			writeConfig(t, root, "mockstro.local.yaml", local)
			sharedPath := filepath.Join(root, ".maestro/mockstro.yaml")
			shared, _ := os.ReadFile(sharedPath)
			p, path, err := SetAgentDevice(root, "android", "Personal_AVD", true)
			if err != nil || p.LocalVariables["USER"] != "local" {
				t.Fatal(p, err)
			}
			if ownsList && !strings.HasSuffix(path, "mockstro.local.yaml") || !ownsList && !strings.HasSuffix(path, "mockstro.yaml") {
				t.Fatal("edited the wrong config", path)
			}
			p, _, err = SetAgentDevice(root, "android", "Personal_AVD", false)
			if err != nil {
				t.Fatal(err)
			}
			if ownsList {
				if len(p.Devices) != 0 {
					t.Fatal("empty local list fell back to shared devices", p.Devices)
				}
				got, _ := os.ReadFile(sharedPath)
				if string(got) != string(shared) {
					t.Fatal("edited shared config despite a local override")
				}
			} else {
				got, _ := os.ReadFile(filepath.Join(root, ".maestro/mockstro.local.yaml"))
				if string(got) != local || !slices.Equal(p.Devices, []string{"Shared"}) {
					t.Fatal("changed local settings", string(got), p.Devices)
				}
			}
		})
	}
}

func TestSetAgentDeviceEditsLocalOnlyPlatform(t *testing.T) {
	root := t.TempDir()
	shared := "platforms: {android: {appId: example.android}}\n"
	writeConfig(t, root, "mockstro.yaml", shared)
	writeConfig(t, root, "mockstro.local.yaml", "platforms:\n  ios:\n    appId: example.ios # local only\n    project: ../ios\n")
	for _, allow := range []bool{true, false} {
		p, path, err := SetAgentDevice(root, "ios", "My iPhone", allow)
		if err != nil || !strings.HasSuffix(path, "mockstro.local.yaml") {
			t.Fatal("local-only platform was not edited locally", path, err)
		}
		if slices.Contains(p.Targets["ios"].Devices, "My iPhone") != allow {
			t.Fatal("agent access was not updated", p.Targets["ios"])
		}
	}
	got, _ := os.ReadFile(filepath.Join(root, ".maestro/mockstro.yaml"))
	if string(got) != shared {
		t.Fatal("local-only platform leaked into shared config", string(got))
	}
	got, _ = os.ReadFile(filepath.Join(root, ".maestro/mockstro.local.yaml"))
	if !strings.Contains(string(got), "# local only") || !strings.Contains(string(got), "devices: []") {
		t.Fatal("local platform comments or empty devices list lost", string(got))
	}
}

func TestSetAgentDeviceRejectsInvalidOrSharedAnchorEdits(t *testing.T) {
	for _, config := range []string{
		"platforms: [invalid]\n",
		"platforms: {ios: {appId: x}}\n",
		"platforms: {android: {appId: x, devices: &shared [Shared]}, ios: {appId: y, devices: *shared}}\n",
	} {
		root := t.TempDir()
		writeConfig(t, root, "mockstro.yaml", config)
		if _, _, err := SetAgentDevice(root, "android", "New", true); err == nil {
			t.Fatalf("accepted unsafe edit: %s", config)
		}
		got, _ := os.ReadFile(filepath.Join(root, ".maestro/mockstro.yaml"))
		if string(got) != config {
			t.Fatal("failed edit changed the file")
		}
	}
}
