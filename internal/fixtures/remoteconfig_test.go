package fixtures

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFileRemoteConfigUsesLiveEditValidationPolicy(t *testing.T) {
	for _, properties := range []string{
		"    bad/key: \"value\"\n",
		"    K: \"" + strings.Repeat("x", (64<<10)+1) + "\"\n",
	} {
		path := filepath.Join(t.TempDir(), "invalid.yaml")
		write(t, path, "appId: app\nproperties:\n  fbrcOverrides:\n"+properties+"---\n- back\n")
		h, err := ParseHeader(path)
		if err == nil {
			err = h.validateProperties(path)
		}
		if err == nil {
			t.Fatal("file accepted values rejected by live edits")
		}
	}
}
