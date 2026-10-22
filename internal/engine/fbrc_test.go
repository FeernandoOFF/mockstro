package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/feernandooff/mockstro/internal/mockruntime"
	"github.com/feernandooff/mockstro/internal/platform"
	"github.com/feernandooff/mockstro/internal/project"
)

func TestRemoteConfigDeliverySelectsAttachedOverrideOnly(t *testing.T) {
	for _, tc := range []struct {
		name, device     string
		auto, wantDevice bool
	}{
		{"server only", "", false, false},
		{"server only with auto enabled", "", true, false},
		{"attached map remote", "device", false, false},
		{"attached override", "device", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Run{Device: tc.device, overrideAuto: tc.auto}
			delivery := r.RemoteConfigDelivery()
			if delivery.UsesDeviceOverride() != tc.wantDevice {
				t.Fatal("wrong transport")
			}
			mappings := delivery.Mappings(map[string]string{"K": "value"})
			if (len(mappings) == 0) != tc.wantDevice {
				t.Fatalf("unexpected HTTP mappings: %v", mappings)
			}
		})
	}
}
func TestStartMockValidatesBeforeAcquiringResources(t *testing.T) {
	session, err := (&Engine{}).StartMock(context.Background(), &mockruntime.Target{FBRC: map[string]string{"bad key": "value"}})
	if err == nil || session != nil {
		t.Fatal("invalid values reached setup")
	}
}

type deliveryPlatform struct {
	platform.Platform
	err    error
	values map[string]string
}

func (p *deliveryPlatform) WriteOverride(_ context.Context, _ string, override platform.Override) error {
	p.values = override.FBRC
	return p.err
}
func TestDeviceDeliveryTracksOnlySuccessfulOverrides(t *testing.T) {
	fail := errors.New("device write failed")
	plat := &deliveryPlatform{err: fail}
	r := &Run{e: &Engine{P: &project.Project{}, Plat: plat}, Device: "device", overrideAuto: true}
	values := map[string]string{"K": "v"}
	if err := r.RemoteConfigDelivery().Deliver(context.Background(), values); !errors.Is(err, fail) {
		t.Fatalf("device error=%v", err)
	}
	if r.overrideApplied {
		t.Fatal("failed device write marked applied")
	}
	plat.err = nil
	if err := r.RemoteConfigDelivery().Deliver(context.Background(), values); err != nil {
		t.Fatal(err)
	}
	if !r.overrideApplied || plat.values["K"] != "v" {
		t.Fatal("successful delivery was not recorded")
	}
	plat.values["K"] = "adapter"
	if values["K"] != "v" {
		t.Fatal("device delivery aliases input")
	}
}
