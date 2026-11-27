package procreap

import (
	"os/exec"
	"testing"
)

func TestParseSplitsReapedAndSurvivors(t *testing.T) {
	got := Parse("noise\nreap pid=12 rss_kb=900 cmd=node server.js\nsurvivor pid=13 cmd=sleep 600\n")
	if len(got) != 2 || got[0].Survivor || got[0].Process != "pid=12 rss_kb=900 cmd=node server.js" ||
		!got[1].Survivor || got[1].Process != "pid=13 cmd=sleep 600" {
		t.Fatalf("parse: %+v", got)
	}
}

func TestEnvironments(t *testing.T) {
	m := WithKeep(Matching("OCTOPUS_RUN_INSTANCE", "abc"), "/run/init/*/pid")
	if m[envMode] != "run" || m[envVar] != "OCTOPUS_RUN_INSTANCE" || m[envValue] != "abc" || m[envKeep] != "/run/init/*/pid" {
		t.Fatalf("matching: %v", m)
	}
	if a := All(""); a[envMode] != "all" || a[envKeep] != "" {
		t.Fatalf("all: %v", a)
	}
}

// The script runs in Linux containers only; this checks bash can parse it.
func TestScriptParses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	if out, err := exec.Command(bash, "-n", "-c", Script()).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}
