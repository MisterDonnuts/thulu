package stage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryIconHasASpriteSymbol(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "components", "IconSprite.astro")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("sprite not readable (%v) — nothing to check against", err)
	}
	sprite := string(raw)
	for _, icon := range TaskTypeIcons {
		if !strings.Contains(sprite, `<symbol id="i-tt-`+icon+`"`) {
			t.Errorf("icon %q is on the allowlist but has no <symbol id=\"i-tt-%s\"> in %s",
				icon, icon, path)
		}
	}
}

func TestValidTaskTypeIcon(t *testing.T) {
	if !ValidTaskTypeIcon("bug") {
		t.Error(`ValidTaskTypeIcon("bug") = false, want true`)
	}
	for _, bad := range []string{"", "skull", "BUG", "../etc/passwd"} {
		if ValidTaskTypeIcon(bad) {
			t.Errorf("ValidTaskTypeIcon(%q) = true, want false", bad)
		}
	}
}
