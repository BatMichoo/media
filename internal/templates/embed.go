// Package templates holds the media app's page templates, built into the
// binary. They're content for the theme's page shell (theme/page).
package templates

import "embed"

//go:embed pages/*.html components/*.html
var FS embed.FS
