// Package static holds the media app's own stylesheet and scripts.
package static

import "embed"

//go:embed css
var FS embed.FS
