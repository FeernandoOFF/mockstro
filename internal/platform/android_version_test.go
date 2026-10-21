package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"
)

func TestAVDVersionReadsRelocatedAVDAndSDKMetadata(t *testing.T) {
	avds, sdk, relocated := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("ANDROID_AVD_HOME", avds)
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(avds, "Custom.ini"), "path="+relocated+"\n")
	write(filepath.Join(relocated, "config.ini"), "image.sysdir.1 = system-images/android-37.0/google_apis/arm64-v8a/\n")
	write(filepath.Join(sdk, "platforms/android-37.0/source.properties"), "Platform.Version=17\n")
	a := &Android{P: &project.Project{Env: map[string]string{"ANDROID_HOME": sdk}}}
	if got := a.AVDVersion("Custom"); got != "Android 17 (API 37.0)" {
		t.Fatal(got)
	}
	write(filepath.Join(relocated, "config.ini"), "target=android-30\n")
	if got := a.AVDVersion("Custom"); got != "Android API 30" {
		t.Fatal(got)
	}
	if got := a.AVDVersion("Missing"); got != "" {
		t.Fatal(got)
	}
}

func TestAndroidDeviceVersionUsesRunningSystem(t *testing.T) {
	a := &Android{Exec: &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		if c.String() != "adb -s emulator-5554 shell getprop ro.build.version.release" {
			t.Fatal(c)
		}
		return "11", nil
	}}}
	if got := a.DeviceVersion(context.Background(), "emulator-5554"); got != "Android 11" {
		t.Fatal(got)
	}
}
