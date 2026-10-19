// Package skills carries the canonical agent skill in every mockstro binary.
package skills

import "embed"

// Files is the skill shipped with this CLI version.
//
//go:embed mockstro
var Files embed.FS
