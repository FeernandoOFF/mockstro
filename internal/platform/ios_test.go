package platform

import (
	"context"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
)

func TestEnsureBootedRespectsHeadlessOnRunningSimulator(t *testing.T) {
	for _, headless := range []bool{true, false} {
		opened := 0
		i := &IOS{Exec: &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
			if c.Name == "open" {
				opened++
				return "", nil
			}
			return `{"devices":{"iOS":[{"udid":"sim","name":"iPhone","state":"Booted","isAvailable":true}]}}`, nil
		}}}
		if err := i.EnsureBooted(context.Background(), "sim", headless, func(string) {}); err != nil {
			t.Fatal(err)
		}
		if headless && opened != 0 || !headless && opened != 1 {
			t.Fatalf("headless=%v opened Simulator.app %d times", headless, opened)
		}
	}
}
