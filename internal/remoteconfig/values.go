// Package remoteconfig defines the values and delivery rules shared by fixtures and runs.
package remoteconfig

import (
	"context"
	"fmt"
	"maps"
	"regexp"

	"github.com/feernandooff/mockstro/internal/wiremock"
)

const (
	MaxKeys   = 500
	MaxValue  = 64 << 10
	MaxKeyLen = 200
)

var key = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Copy always returns an independent, non-nil set. Missing keys use app defaults.
func Copy(values map[string]string) map[string]string { return Merge(values) }

// Merge applies layers in order; later values replace earlier values.
func Merge(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		maps.Copy(out, layer)
	}
	return out
}
func Validate(values map[string]string) (map[string]string, error) {
	if len(values) > MaxKeys {
		return nil, fmt.Errorf("at most %d Remote Config values are supported", MaxKeys)
	}
	for k, v := range values {
		if k == "" || len(k) > MaxKeyLen || !key.MatchString(k) {
			return nil, fmt.Errorf("%q is not a valid Remote Config key; use letters, digits, _ . and -", k)
		}
		if len(v) > MaxValue {
			return nil, fmt.Errorf("the value of %s is larger than %d KB", k, MaxValue>>10)
		}
	}
	return Copy(values), nil
}

// Delivery is the selected transport for a run. Mappings participates in full activation;
// Deliver updates values in place. Device delivery contributes no HTTP mappings.
type Delivery interface {
	UsesDeviceOverride() bool
	Mappings(map[string]string) []any
	Deliver(context.Context, map[string]string) error
}
type MappingWriter interface {
	ReplaceMapping(string, any) error
}
type HTTP struct{ Writer MappingWriter }

func (HTTP) UsesDeviceOverride() bool           { return false }
func (HTTP) Mappings(v map[string]string) []any { return wiremock.FBRCMappings(v) }
func (d HTTP) Deliver(ctx context.Context, v map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.Writer.ReplaceMapping(wiremock.FBRCFetchID, wiremock.FBRCFetchMapping(v))
}

type Device struct {
	Write func(context.Context, map[string]string) error
}

func (Device) UsesDeviceOverride() bool                                 { return true }
func (Device) Mappings(map[string]string) []any                         { return nil }
func (d Device) Deliver(ctx context.Context, v map[string]string) error { return d.Write(ctx, Copy(v)) }
