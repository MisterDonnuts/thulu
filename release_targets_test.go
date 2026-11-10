package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// A RELEASE_IMAGES spec contains `|`; unquoted, bash reads it as a pipe and the release target dies
// on its first line.
func TestReleaseTargetsParseInBash(t *testing.T) {
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not installed")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	for _, target := range []string{"release-images", "release-scan"} {
		out, err := exec.Command(makeBin, "-n", "--no-print-directory", target,
			"REGISTRY=example.test", "VERSION=0.0.0").CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %s: %v\n%s", target, err, out)
		}
		if !strings.Contains(string(out), "octopus-agent-claude-code") {
			t.Fatalf("make -n %s does not name the images:\n%s", target, out)
		}
		check := exec.Command(bash, "-n")
		check.Stdin = bytes.NewReader(out)
		if msg, err := check.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse in bash: %v\n%s", target, err, msg)
		}
	}
}
