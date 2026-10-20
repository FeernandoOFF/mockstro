package platform

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
)

func TestDevicesKeepsWirelessSerials(t *testing.T) {
	out := "List of devices attached\nadb-34091JEHN03468-2jsyab (2)._adb-tls-connect._tcp\tdevice\nemulator-5554\tdevice\nR5CT\toffline\n\n"
	a := &Android{Exec: &execx.Fake{Handler: func(execx.Cmd) (string, error) { return out, nil }}}
	devs, err := a.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || devs[0].Serial != "adb-34091JEHN03468-2jsyab (2)._adb-tls-connect._tcp" || devs[0].Class != "physical" || devs[1].Class != "emulator" {
		t.Fatalf("devices %+v", devs)
	}
}

func TestSettleWaitsForThreeIdleSamples(t *testing.T) {
	// Cumulative /proc/stat: each step adds 100 ticks, busy for the first two, then idle.
	var busy, idle uint64
	steps := []uint64{90, 80, 10, 10, 50, 10, 10, 10}
	calls := 0
	a := &Android{settleTick: time.Millisecond, Exec: &execx.Fake{Handler: func(execx.Cmd) (string, error) {
		if calls > 0 && calls <= len(steps) {
			b := steps[calls-1]
			busy, idle = busy+b, idle+100-b
		}
		calls++
		return fmt.Sprintf("cpu  %d 0 0 %d 0 0 0 0 0 0", busy, idle), nil
	}}}
	var logs []string
	a.settle(context.Background(), "emulator-5554", func(s string) { logs = append(logs, s) })
	// Samples: busy, busy, idle, idle, busy (resets), idle, idle, idle -> settled on the 8th.
	if calls != 9 || !strings.Contains(logs[len(logs)-1], "settled") {
		t.Fatalf("calls %d logs %v", calls, logs)
	}
}

func TestSettleStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	a := &Android{settleTick: time.Hour, Exec: &execx.Fake{Handler: func(execx.Cmd) (string, error) {
		calls++
		cancel()
		return "cpu 100 0 0 100 0 0 0 0 0 0", nil
	}}}
	a.settle(ctx, "emulator-5554", func(string) {})
	if calls != 1 {
		t.Fatalf("sampled a cancelled device %d times", calls)
	}
}

func TestCPUTimesDoesNotDoubleCountGuestTime(t *testing.T) {
	a := &Android{Exec: &execx.Fake{Handler: func(execx.Cmd) (string, error) {
		return "cpu 100 0 20 80 10 0 0 0 30 0", nil
	}}}
	busy, total, ok := a.cpuTimes(context.Background(), "emulator-5554")
	if !ok || busy != 130 || total != 210 {
		t.Fatalf("busy=%d total=%d ok=%v", busy, total, ok)
	}
}
