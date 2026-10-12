// Package web embeds the static UI.
package web

import "embed"

//go:embed index.html app.css app.js fonts
var FS embed.FS
