package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
)

var adapterFuncs = []string{"engine_run", "engine_session_save", "engine_skills_dir"}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func registered() []string {
	names := engine.Known()
	sort.Strings(names)
	return names
}

func TestConformance_ShellAdapter(t *testing.T) {
	enginesDir := filepath.Join(repoRoot(t), "images", "snippets", "engines")
	for _, name := range registered() {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(enginesDir, name+".sh"))
			if err != nil {
				t.Fatalf("adapter missing: %v", err)
			}
			for _, fn := range adapterFuncs {
				def := regexp.MustCompile(`(?m)^\s*(function\s+)?` + fn + `\s*\(\)`)
				if !def.Match(src) {
					t.Errorf("%s.sh does not define %s()", name, fn)
				}
			}
		})
	}
}

func TestConformance_EveryAdapterHasAnEngine(t *testing.T) {
	enginesDir := filepath.Join(repoRoot(t), "images", "snippets", "engines")
	adapters, err := filepath.Glob(filepath.Join(enginesDir, "*.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range adapters {
		name := strings.TrimSuffix(filepath.Base(path), ".sh")
		if _, err := engine.Get(name); err != nil {
			t.Errorf("adapter %s has no registered engine (blank-import it in this file): %v", filepath.Base(path), err)
		}
	}
}

func TestConformance_Fixtures(t *testing.T) {
	for _, name := range registered() {
		eng := engine.MustGet(name)
		cases := []string{engine.OutcomeOK, engine.OutcomeError}
		if eng.Capabilities().TurnBudget {
			cases = append(cases, engine.OutcomeMaxTurns)
		}
		for _, want := range cases {
			t.Run(name+"/"+want, func(t *testing.T) {
				f, err := os.Open(filepath.Join("testdata", name, want+".jsonl"))
				if errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("fixture testdata/%s/%s.jsonl missing", name, want)
				}
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()

				var init, result *engine.Event
				results := 0
				for ev := range eng.ParseStream(f) {
					switch ev.Kind {
					case engine.EventInit:
						if init == nil {
							e := ev
							init = &e
						}
					case engine.EventResult:
						results++
						e := ev
						result = &e
					default:
					}
				}
				if init == nil || init.SessionID == "" {
					t.Errorf("no EventInit with a SessionID: %+v", init)
				}
				if init != nil && init.SessionMode == "" {
					t.Errorf("the adapter's octopus.session line did not reach EventInit.SessionMode")
				}
				if results != 1 {
					t.Fatalf("got %d EventResult, want exactly 1", results)
				}
				if result.Outcome != want {
					t.Errorf("Outcome = %q, want %q", result.Outcome, want)
				}
				if want != engine.OutcomeMaxTurns && result.IsError != (want == engine.OutcomeError) {
					t.Errorf("IsError = %v disagrees with Outcome %q", result.IsError, result.Outcome)
				}
			})
		}
	}
}

func TestConformance_DefaultModelIsPriced(t *testing.T) {
	for _, name := range registered() {
		t.Run(name, func(t *testing.T) {
			caps := engine.MustGet(name).Capabilities()
			if caps.DefaultModel == "" {
				t.Fatal("Capabilities.DefaultModel is empty")
			}
			priced := false
			for _, p := range caps.Pricing {
				if p.Model == caps.DefaultModel ||
					(p.Family && strings.Contains(strings.ToLower(caps.DefaultModel), strings.ToLower(p.Model))) {
					priced = true
					break
				}
			}
			if !priced {
				t.Errorf("no Pricing row for the default model %q", caps.DefaultModel)
			}
			for _, p := range caps.Pricing {
				if p.InPerMTok <= 0 || p.OutPerMTok <= 0 {
					t.Errorf("price for %q has no input/output rate: %+v", p.Model, p)
				}
			}
		})
	}
}

type isolation struct {
	switches []string
	gap      string
}

var isolationByEngine = map[string]isolation{
	"claude-code": {
		switches: []string{"--strict-mcp-config"},
	},
	"codex": {
		gap: "codex.sh runs with whatever $CODEX_HOME/config.toml holds (MCP servers, providers, " +
			"profiles) and the AGENTS.md files codex finds from the working directory; it sets no " +
			"switch that keeps them out",
	},
	"opencode": {
		switches: []string{
			"OPENCODE_CONFIG_CONTENT=",
			"OPENCODE_AUTH_CONTENT='{}'",
			"OPENCODE_DISABLE_PROJECT_CONFIG=1",
			"OPENCODE_PURE=1",
			"OPENCODE_DISABLE_CLAUDE_CODE=1",
			"OPENCODE_DISABLE_EXTERNAL_SKILLS=1",
			"unset OPENCODE_CONFIG OPENCODE_CONFIG_DIR",
			"oc_isolate",
		},
	},
}

func TestConformance_IsolatedConfig(t *testing.T) {
	enginesDir := filepath.Join(repoRoot(t), "images", "snippets", "engines")
	for _, name := range registered() {
		t.Run(name, func(t *testing.T) {
			want, ok := isolationByEngine[name]
			if !ok {
				t.Fatalf("no isolation requirement for %s: add its row to isolationByEngine", name)
			}
			src, err := os.ReadFile(filepath.Join(enginesDir, name+".sh"))
			if err != nil {
				t.Fatalf("adapter missing: %v", err)
			}
			var missing []string
			for _, sw := range want.switches {
				if !strings.Contains(string(src), sw) {
					missing = append(missing, sw)
				}
			}
			isolated := len(want.switches) > 0 && len(missing) == 0
			switch {
			case isolated && want.gap != "":
				t.Logf("%s.sh now has every switch: drop the gap from isolationByEngine", name)
			case isolated:
			case want.gap != "":
				t.Skipf("known gap: %s", want.gap)
			default:
				t.Errorf("%s.sh lacks %q", name, missing)
			}
		})
	}
}
