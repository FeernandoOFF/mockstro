package remoteconfig

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMergeCopiesLayersAndValidationPolicy(t *testing.T) {
	base := map[string]string{"K": "base", "Other": "kept"}
	override := map[string]string{"K": "override"}
	merged := Merge(base, override)
	if merged["K"] != "override" || merged["Other"] != "kept" {
		t.Fatal(merged)
	}
	merged["K"] = "changed"
	if override["K"] != "override" || base["K"] != "base" {
		t.Fatal("merge aliases input")
	}
	for _, v := range []map[string]string{{"bad key": "x"}, {"K": strings.Repeat("x", MaxValue+1)}} {
		if _, err := Validate(v); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	copy, err := Validate(base)
	if err != nil {
		t.Fatal(err)
	}
	copy["K"] = "copy"
	if base["K"] != "base" {
		t.Fatal("validate aliases input")
	}
}

type writer struct {
	calls int
	err   error
}

func (w *writer) ReplaceMapping(string, any) error { w.calls++; return w.err }
func TestActualDeliveryAdaptersPropagateFailures(t *testing.T) {
	fail := errors.New("failed")
	w := &writer{err: fail}
	http := HTTP{Writer: w}
	if http.UsesDeviceOverride() || len(http.Mappings(nil)) == 0 {
		t.Fatal("HTTP delivery has no mappings")
	}
	if !errors.Is(http.Deliver(context.Background(), nil), fail) || w.calls != 1 {
		t.Fatal("HTTP failure swallowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = http.Deliver(ctx, nil)
	if w.calls != 1 {
		t.Fatal("cancelled delivery reached server")
	}
	values := map[string]string{"K": "v"}
	var got map[string]string
	device := Device{Write: func(_ context.Context, v map[string]string) error { got = v; v["K"] = "mutated"; return fail }}
	if !device.UsesDeviceOverride() || len(device.Mappings(values)) != 0 {
		t.Fatal("device delivery contributes HTTP mappings")
	}
	if !errors.Is(device.Deliver(context.Background(), values), fail) {
		t.Fatal("device failure swallowed")
	}
	if !reflect.DeepEqual(values, map[string]string{"K": "v"}) || got["K"] != "mutated" {
		t.Fatal("device adapter aliases caller values")
	}
}
