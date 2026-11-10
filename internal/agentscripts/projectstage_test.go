package agentscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func projectStageScript(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(root, "images", "snippets", "project-stage.sh")
}

func i18nScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(filepath.Dir(projectStageScript(t)), "i18n.sh")
}

func bashOrSkip(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	return bash
}

func writeHook(t *testing.T, root, proj, stage, body string) {
	t.Helper()
	dir := filepath.Join(root, proj, ".thulu", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stage+".sh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runProjectStage(t *testing.T, projectsRoot, stage string) (string, time.Duration) {
	t.Helper()
	bash := bashOrSkip(t)
	tmp := t.TempDir()
	merged := filepath.Join(tmp, "merged.md")
	outDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(merged, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wrapper := "set -e; source " + i18nScript(t) + "; source " + projectStageScript(t)
	cmd := exec.Command(bash, "-c", wrapper)
	cmd.Env = append(os.Environ(),
		"OCTOPUS_LOCALE=en",
		"STAGE="+stage,
		"MERGED="+merged,
		"OUTPUT_DIR="+outDir,
		"PROJECTS_ROOT="+projectsRoot,
	)
	start := time.Now()
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("project-stage.sh: %v\n%s", err, b)
	}
	elapsed := time.Since(start)
	got, err := os.ReadFile(merged)
	if err != nil {
		t.Fatal(err)
	}
	return string(got), elapsed
}

func TestProjectStageHooksRunInParallel(t *testing.T) {
	bashOrSkip(t)
	root := t.TempDir()
	writeHook(t, root, "alpha", "in_progress", "#!/usr/bin/env bash\nsleep 0.5\necho alpha-done\n")
	writeHook(t, root, "bravo", "in_progress", "#!/usr/bin/env bash\nsleep 0.5\necho bravo-done\n")

	merged, elapsed := runProjectStage(t, root, "in_progress")

	if elapsed > 900*time.Millisecond {
		t.Errorf("hooks took %s — appear to run serially (serial sum ~1.0s)", elapsed)
	}
	if !strings.Contains(merged, "alpha-done") || !strings.Contains(merged, "bravo-done") {
		t.Errorf("both hook outputs should be injected:\n%s", merged)
	}
	if !strings.Contains(merged, "exit=0") {
		t.Errorf("hook exit code should be reported:\n%s", merged)
	}
}

func TestProjectStageDeterministicOrder(t *testing.T) {
	bashOrSkip(t)
	root := t.TempDir()
	writeHook(t, root, "alpha", "testing", "#!/usr/bin/env bash\nsleep 0.4\necho alpha-ran\n")
	writeHook(t, root, "bravo", "testing", "#!/usr/bin/env bash\nsleep 0.05\necho bravo-ran\n")

	merged, _ := runProjectStage(t, root, "testing")
	ai := strings.Index(merged, "[alpha] Hook")
	bi := strings.Index(merged, "[bravo] Hook")
	if ai < 0 || bi < 0 {
		t.Fatalf("both hook sections expected:\n%s", merged)
	}
	if ai > bi {
		t.Errorf("injection order not deterministic: alpha@%d after bravo@%d", ai, bi)
	}
}

func TestProjectStageHookFailureNonFatal(t *testing.T) {
	bashOrSkip(t)
	root := t.TempDir()
	writeHook(t, root, "alpha", "in_progress", "#!/usr/bin/env bash\necho boom; exit 3\n")

	merged, _ := runProjectStage(t, root, "in_progress")
	if !strings.Contains(merged, "exit=3") {
		t.Errorf("failing hook exit code not reported:\n%s", merged)
	}
	if !strings.Contains(merged, "boom") {
		t.Errorf("failing hook output not captured:\n%s", merged)
	}
}

func TestProjectStageNoProjectsNoop(t *testing.T) {
	bashOrSkip(t)
	merged, _ := runProjectStage(t, filepath.Join(t.TempDir(), "does-not-exist"), "in_progress")
	if strings.TrimSpace(merged) != "" {
		t.Errorf("expected empty merged prompt, got:\n%s", merged)
	}
}
