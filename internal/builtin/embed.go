package builtin

import (
	"embed"
	"io/fs"

	"github.com/MisterDonnuts/thulu/tools"
)

//go:embed all:system-skills
var systemSkills embed.FS

//go:embed all:workshop
var workshop embed.FS

//go:embed starter/pack.json
var starterPack []byte

func SystemSkills() fs.FS {
	sub, err := fs.Sub(systemSkills, "system-skills")
	if err != nil {
		panic(err)
	}
	return sub
}

func Workshop() fs.FS {
	sub, err := fs.Sub(workshop, "workshop")
	if err != nil {
		panic(err)
	}
	return sub
}

func StarterPack() []byte { return starterPack }

func NativeTools() fs.FS { return tools.Native() }
