package engine

import (
	"context"
	"fmt"
	"maps"
	"regexp"

	"github.com/feernandooff/mockstro/internal/wiremock"
)

const (
	maxFBRCKeys   = 500
	maxFBRCValue  = 64 << 10
	maxFBRCKeyLen = 200
)

var fbrcKey = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidateFBRC checks a set of Remote Config values a tester typed and returns a copy of it.
func ValidateFBRC(values map[string]string) (map[string]string, error) {
	if len(values) > maxFBRCKeys {
		return nil, fmt.Errorf("at most %d Remote Config values are supported", maxFBRCKeys)
	}
	for k, v := range values {
		if k == "" || len(k) > maxFBRCKeyLen || !fbrcKey.MatchString(k) {
			return nil, fmt.Errorf("%q is not a valid Remote Config key; use letters, digits, _ . and -", k)
		}
		if len(v) > maxFBRCValue {
			return nil, fmt.Errorf("the value of %s is larger than %d KB", k, maxFBRCValue>>10)
		}
	}
	return maps.Clone(values), nil
}

// FBRC is the Remote Config the app is being served now, which may differ from the target's own.
func (m *MockSession) FBRC() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return maps.Clone(m.fbrc)
}

// SetFBRC serves values as the app's Remote Config from the next fetch on. Only the Remote Config
// answer changes: the request journal, custom responses and every other stub stay as they are.
// A key left out is absent, so the app falls back to its built-in default for it.
func (m *MockSession) SetFBRC(ctx context.Context, values map[string]string) error {
	clean, err := ValidateFBRC(values)
	if err != nil {
		return err
	}
	m.ovMu.Lock()
	defer m.ovMu.Unlock()
	r := m.Run
	if r.Device != "" && r.overrideAuto {
		// Attached device: Remote Config rides in the override the app reads.
		if err := m.e.Plat.WriteOverride(ctx, r.Device, r.override(clean)); err != nil {
			return err
		}
		r.overrideApplied = true
	} else if err := r.WM.ReplaceMapping(wiremock.FBRCFetchID, wiremock.FBRCFetchMapping(clean)); err != nil {
		return err
	}
	m.mu.Lock()
	m.fbrc = clean
	m.mu.Unlock()
	return nil
}

// ResetFBRC puts back the Remote Config the target defines.
func (m *MockSession) ResetFBRC(ctx context.Context) error {
	return m.SetFBRC(ctx, m.Target().FBRC)
}
