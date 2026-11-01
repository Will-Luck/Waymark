// Package assets embeds the page templates and the static files (CSS, JS
// modules, fonts, icon sprite) so the binary carries the whole web layer.
package assets

import "embed"

// FS holds templates/ and static/.
//
//go:embed templates static
var FS embed.FS
