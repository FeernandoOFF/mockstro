package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

func (a *Android) DeviceVersion(ctx context.Context, serial string) string {
	version, err := a.adb(ctx, serial, "shell", "getprop", "ro.build.version.release")
	if err != nil || version == "" {
		return ""
	}
	return "Android " + version
}

// AVDVersion reads SDK metadata without starting the emulator.
func (a *Android) AVDVersion(name string) string {
	avdHome := os.Getenv("ANDROID_AVD_HOME")
	if avdHome == "" {
		userHome := os.Getenv("ANDROID_USER_HOME")
		if userHome == "" {
			userHome = filepath.Join(os.Getenv("HOME"), ".android")
		}
		avdHome = filepath.Join(userHome, "avd")
	}
	dir := readProperty(filepath.Join(avdHome, name+".ini"), "path")
	if dir == "" {
		dir = filepath.Join(avdHome, name+".avd")
	}
	config := filepath.Join(dir, "config.ini")
	target := readProperty(config, "target")
	if target == "" {
		for _, part := range strings.Split(readProperty(config, "image.sysdir.1"), "/") {
			if strings.HasPrefix(part, "android-") {
				target = part
				break
			}
		}
	}
	api := strings.TrimPrefix(target, "android-")
	if api == "" {
		return ""
	}
	for _, sdk := range []string{a.P.Env["ANDROID_HOME"], a.P.Env["ANDROID_SDK_ROOT"], filepath.Join(os.Getenv("HOME"), "Library", "Android", "sdk"), filepath.Join(os.Getenv("HOME"), "Android", "Sdk")} {
		if sdk == "" {
			continue
		}
		if version := readProperty(filepath.Join(sdk, "platforms", target, "source.properties"), "Platform.Version"); version != "" {
			return "Android " + version + " (API " + api + ")"
		}
	}
	return "Android API " + api
}

func readProperty(path, key string) string {
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(data), "\n") {
		if k, value, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
