// Package tools carries the descriptors and manuals of the MCP servers that
// ship with Thulu — tool.yaml and skill.md of each — as data inside the
// daemon binary.
//
// Without them no install has the descriptors: the daemon image copies no
// tools/ directory and the agent store holds only what an operator imported.
// Embedding the files here keeps a single source of truth: the copy in the
// binary is these files, not a generated duplicate.
//
// The patterns name each file. A directory pattern would sweep in
// node_modules/ of whoever ran npm install in a server's directory, and a glob
// over */tool.yaml would ship any scratch server someone dropped next to them.
// embed_test.go checks that every server directory with a tool.yaml is listed.
package tools

import (
	"embed"
	"io/fs"
)

//go:embed thulu-browser/tool.yaml thulu-browser/skill.md
//go:embed thulu-cloud/tool.yaml thulu-cloud/skill.md
//go:embed thulu-figma/tool.yaml thulu-figma/skill.md
//go:embed thulu-github/tool.yaml thulu-github/skill.md
//go:embed thulu-sandbox/tool.yaml thulu-sandbox/skill.md
//go:embed octopus-api/tool.yaml
var native embed.FS

// Native returns the shipped servers' files, one directory per server:
// <name>/tool.yaml, and <name>/skill.md when the server has a manual.
// octopus-api has none: the system skills with `mcp: octopus-api` teach it.
func Native() fs.FS { return native }
