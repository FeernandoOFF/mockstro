package platform

import (
	"context"
	"testing"

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
