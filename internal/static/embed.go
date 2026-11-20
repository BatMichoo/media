// Package static holds the media app's own stylesheet, scripts and icon
// (img/favicon.png, drawn from img/favicon.svg).
package static

import "embed"

//go:embed css js img
var FS embed.FS
