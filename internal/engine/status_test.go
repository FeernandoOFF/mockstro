package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
)

func TestStatusListsUndesignatedStoppedEmulators(t *testing.T) {
	for _, owner := range []string{"human", "agent"} {
		t.Run(owner, func(t *testing.T) {
			e, fake := testEngine(t, owner, func(c execx.Cmd) (string, error) {
				if len(c.Args) == 1 && c.Args[0] == "-list-avds" {
					return "Stopped\nAgentStopped\nRunning\nStopped\n", nil
				}
				if out, ok := adbDevices(c, map[string]string{"emulator-5554": "Running"}); ok {
					return out, nil
				}
				return "", nil
			})
			fake.Paths["adb"], fake.Paths["emulator"] = "/bin/adb", "/bin/emulator"
			e.P.Env["MAESTRO_AGENT_AVDS"] = "AgentStopped,Running"
			st := e.Status(context.Background())
			if len(st.Devices) != 1 || len(st.Emulators) != 2 || st.Emulators[0].Name != "AgentStopped" || !st.Emulators[0].Designated || st.Emulators[1].Name != "Stopped" || st.Emulators[1].Designated {
				t.Fatalf("inventory: devices=%+v emulators=%+v", st.Devices, st.Emulators)
			}
			want := []string{"AgentStopped"}
			if owner == "human" {
				want = append(want, "Stopped")
			}
			if !slices.Equal(st.Bootable, want) {
				t.Fatalf("agent boot policy changed: %v", st.Bootable)
			}
		})
	}
}

func TestStatusListsInstalledAVDsWithoutADB(t *testing.T) {
	e, fake := testEngine(t, "human", func(c execx.Cmd) (string, error) { return "Pixel_A\nPixel_B", nil })
	fake.Paths["emulator"] = "/bin/emulator"
	st := e.Status(context.Background())
	if len(st.Devices) != 0 || len(st.Emulators) != 2 {
		t.Fatalf("installed AVDs hidden without adb: %+v", st)
	}
}

func TestStatusListsAllStoppedIOSSimulators(t *testing.T) {
	e, fake := testEngine(t, "agent", func(c execx.Cmd) (string, error) {
		if c.String() == "xcrun simctl list devices available -j" {
			return `{"devices":{"iOS":[{"udid":"running","name":"Running","state":"Booted","isAvailable":true},{"udid":"agent","name":"AgentCold","state":"Shutdown","isAvailable":true},{"udid":"personal","name":"Personal","state":"Shutdown","isAvailable":true}]}}`, nil
		}
		return "", nil
	})
	e.P.Platform, e.P.IOSSimNames = "ios", []string{"AgentCold"}
	o := e.Opts
	o.Platform = "ios"
	e = New(e.P, o, fake, nil)
	fake.Paths["xcrun"] = "/bin/xcrun"
	st := e.Status(context.Background())
	if len(st.Devices) != 1 || len(st.Emulators) != 2 || st.Emulators[1].Name != "Personal" || st.Emulators[1].Designated || !slices.Equal(st.Bootable, []string{"AgentCold"}) {
		t.Fatalf("iOS inventory: %+v", st)
	}
}

func TestStatusKeepsIOSRuntimeVersionsAndBootsSelectedSimulator(t *testing.T) {
	e, fake := testEngine(t, "agent", func(c execx.Cmd) (string, error) {
		switch c.String() {
		case "xcrun simctl list devices available -j":
			return `{"devices":{
			"com.apple.CoreSimulator.SimRuntime.iOS-26-0":[{"udid":"old","name":"iPad","state":"Shutdown","isAvailable":true}],
			"com.apple.CoreSimulator.SimRuntime.iOS-26-3":[{"udid":"running","name":"iPad","state":"Booted","isAvailable":true},{"udid":"new","name":"iPad","state":"Shutdown","isAvailable":true}]
			}}`, nil
		case "xcrun simctl list runtimes -j":
			return `{"runtimes":[{"identifier":"com.apple.CoreSimulator.SimRuntime.iOS-26-0","name":"iOS 26.0","version":"26.0"},{"identifier":"com.apple.CoreSimulator.SimRuntime.iOS-26-3","name":"iOS 26.3","version":"26.3.1"}]}`, nil
		}
		return "", nil
	})
	e.P.Platform, e.P.IOSSimNames = "ios", []string{"iPad"}
	o := e.Opts
	o.Platform = "ios"
	e = New(e.P, o, fake, nil)
	fake.Paths["xcrun"] = "/bin/xcrun"
	st := e.Status(context.Background())
	if len(st.Devices) != 1 || st.Devices[0].OSVersion != "iOS 26.3.1" || len(st.Emulators) != 2 {
		t.Fatalf("same-name runtimes lost: devices=%+v emulators=%+v", st.Devices, st.Emulators)
	}
	if st.Emulators[0].Serial != "old" || st.Emulators[0].OSVersion != "iOS 26.0" || st.Emulators[1].Serial != "new" || st.Emulators[1].OSVersion != "iOS 26.3.1" {
		t.Fatalf("runtime identities lost: %+v", st.Emulators)
	}
	serial, err := e.Plat.Boot(context.Background(), st.Emulators[0].Serial, true, func(string) {})
	if err != nil || serial != "old" || !slices.Contains(fake.Calls, "xcrun simctl boot old") || slices.Contains(fake.Calls, "xcrun simctl boot new") {
		t.Fatalf("booted the wrong runtime: serial=%s err=%v calls=%v", serial, err, fake.Calls)
	}
}

func TestAgentEmulatorDesignationAlsoAppliesToLite(t *testing.T) {
	e, _ := testEngine(t, "agent", func(c execx.Cmd) (string, error) {
		if out, ok := adbDevices(c, map[string]string{"emulator-5554": "Agent", "emulator-5556": "Personal"}); ok {
			return out, nil
		}
		return "", nil
	})
	e.P.Env["MAESTRO_AGENT_AVDS"] = "Agent"
	e.Opts.Lite, e.Opts.MapRemote = true, true
	if !e.Allowed(context.Background(), "emulator-5554") || e.Allowed(context.Background(), "emulator-5556") {
		t.Fatal("lite bypassed emulator designation")
	}
}
