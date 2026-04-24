// Package web embeds the static dashboard assets (HTML/CSS/JS) into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var staticFS embed.FS

// FS returns the embedded static asset filesystem rooted at the directory
// containing index.html. Callers typically wrap it with http.FS for serving.
func FS() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// This is a programmer error — the embedded directory must exist.
		panic(err)
	}
	return sub
}
