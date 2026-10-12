// Package web embeds the static UI.
package web

import "embed"

//go:embed index.html app.css app.js
var FS embed.FS
