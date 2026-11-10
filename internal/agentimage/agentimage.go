package agentimage

import (
	"strings"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

const EnvOverride = "OCTOPUS_AGENT_IMAGE"

const Combined = "octopus-agent"

// Mirrors the engines internal/engine registers (TestEnginesAreTheRegisteredOnes); kept literal so
// the updater and the janitor need not import every engine.
var Engines = []string{"claude-code", "codex", "opencode"}

func Name(eng string) string { return Combined + "-" + eng }

// The _IMAGE suffix is what the updater's kept list collects from the managed block.
func Env(eng string) string {
	return "OCTOPUS_AGENT_" + strings.ToUpper(strings.ReplaceAll(eng, "-", "_")) + "_IMAGE"
}

type Images struct {
	Override string
	ByEngine map[string]string
}

func FromEnv(getenv func(string) string) Images {
	imgs := Images{Override: strings.TrimSpace(getenv(EnvOverride)), ByEngine: map[string]string{}}
	for _, e := range Engines {
		if img := strings.TrimSpace(getenv(Env(e))); img != "" {
			imgs.ByEngine[e] = img
		}
	}
	return imgs
}

func (i Images) For(eng string) (image, env string) {
	if i.Override != "" {
		return i.Override, EnvOverride
	}
	if eng == "" {
		eng = engine.DefaultName
	}
	return i.ByEngine[eng], Env(eng)
}

func (i Images) All() []string {
	var out []string
	if i.Override != "" {
		out = append(out, i.Override)
	}
	for _, e := range Engines {
		if img := i.ByEngine[e]; img != "" {
			out = append(out, img)
		}
	}
	return out
}
