package web

import "embed"

// DistFS contains the built Astro dashboard (web/dist/).
// The daemon mounts this as a static file server so both API and
// dashboard are served from the same address without a separate process.
//
//go:embed all:dist
var DistFS embed.FS
