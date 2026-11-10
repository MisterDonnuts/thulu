package initwizard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type recordingRunner struct{ calls []string }

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil
}

func TestDryRunWritesNothingAndStartsNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	example := "OCTOPUS_DOMAIN=thulu.example.com\nOCTOPUS_JWT_SECRET=   # generate with: openssl rand -hex 32\n"
	if err := os.WriteFile(filepath.Join(root, "deploy", ".env.example"), []byte(example), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	runner := &recordingRunner{}
	answers := "2\n2\n\n3\n\n\n\ns\n\n\n\n\n\n\n\ny\n"
	err := Run(context.Background(), Options{
		Root:     root,
		DryRun:   true,
		Prompter: NewTestPrompter(strings.NewReader(answers), &out),
		Runner:   runner,
		Out:      &out,
		Detect:   func() {},
	})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}

	if len(runner.calls) != 0 {
		t.Errorf("dry run executed commands: %v", runner.calls)
	}
	for _, name := range []string{".env", ".octopus-init.json", "docker-compose.nologin.yml"} {
		if _, statErr := os.Stat(filepath.Join(root, "deploy", name)); statErr == nil {
			t.Errorf("dry run wrote deploy/%s", name)
		}
	}
	if !strings.Contains(out.String(), "Dry run: nothing was written") {
		t.Errorf("no dry-run notice in the output:\n%s", out.String())
	}
	body := out.String()
	if !strings.Contains(body, "docker compose -f deploy/docker-compose.standalone.yml") {
		t.Errorf("preview omits the command it would run:\n%s", body)
	}
	if !strings.Contains(body, "OCTOPUS_DEV_MODE=1") {
		t.Errorf("preview omits the dev-mode switch:\n%s", body)
	}
}

func TestDeclineAtConfirmationIsClean(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runner := &recordingRunner{}
	answers := "2\n2\n\n3\n\n\n\ns\n\n\n\n\n\n\n\nn\n"
	if err := Run(context.Background(), Options{
		Root:     root,
		Prompter: NewTestPrompter(strings.NewReader(answers), &out),
		Runner:   runner,
		Out:      &out,
		Detect:   func() {},
	}); err != nil {
		t.Fatalf("declining returned an error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("ran commands after the operator said no: %v", runner.calls)
	}
	if _, err := os.Stat(filepath.Join(root, "deploy", ".env")); err == nil {
		t.Error("wrote deploy/.env after the operator declined")
	}
}

func TestInputEndingBeforeTheConfirmationInstallsNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runner := &recordingRunner{}
	err := Run(context.Background(), Options{
		Root:     root,
		Prompter: NewTestPrompter(strings.NewReader(""), &out),
		Runner:   runner,
		Out:      &out,
		Detect:   func() {},
	})
	if err == nil || !strings.Contains(err.Error(), "-non-interactive") {
		t.Fatalf("err = %v, want the refusal\n%s", err, out.String())
	}
	if len(runner.calls) != 0 {
		t.Errorf("ran commands nobody confirmed: %v", runner.calls)
	}
	if _, statErr := os.Stat(filepath.Join(root, "deploy", ".env")); statErr == nil {
		t.Error("wrote deploy/.env nobody confirmed")
	}
}

type failingRunner struct{}

func (failingRunner) Run(_ context.Context, name string, args ...string) error {
	return fmt.Errorf("%s %s: exit status 1", name, strings.Join(args, " "))
}

func TestStartStackFailureSaysHowToRetry(t *testing.T) {
	var out bytes.Buffer
	w := &run{
		opts: Options{Root: t.TempDir(), Runner: failingRunner{}},
		p:    NewTestPrompter(strings.NewReader(""), &out),
		out:  &out,
		cfg:  &Config{Mode: ModeDocker, ComposeFile: standaloneComposeFile, Image: "ghcr.io/x/octopus:1"},
	}
	err := w.startStack(context.Background())
	if err == nil {
		t.Fatal("a failed compose up was reported as started")
	}
	text := apperr.Text(err)
	for _, want := range []string{"docker compose -f deploy/docker-compose.standalone.yml up -d", "thulu init"} {
		if !strings.Contains(text, want) {
			t.Errorf("error lacks %q:\n%s", want, text)
		}
	}
}

func TestDockerInstallPullsOnlyTheChosenEnginesImages(t *testing.T) {
	var out bytes.Buffer
	runner := &recordingRunner{}
	w := &run{
		opts: Options{Root: t.TempDir(), Runner: runner},
		p:    NewTestPrompter(strings.NewReader(""), &out),
		out:  &out,
		cfg: &Config{Mode: ModeDocker, Image: "registry.corp.example/octopus/octopus:1.2.0",
			ImageRegistry: "registry.corp.example/octopus", ImageTag: "1.2.0", Engines: []string{"codex"}},
	}
	w.pullAgentImages(context.Background())
	want := "docker pull registry.corp.example/octopus/octopus-agent-codex:1.2.0"
	if len(runner.calls) != 1 || runner.calls[0] != want {
		t.Errorf("calls = %v, want [%s]", runner.calls, want)
	}

	out.Reset()
	w.opts.Runner = failingRunner{}
	w.pullAgentImages(context.Background())
	if !strings.Contains(out.String(), "first run") {
		t.Errorf("a failed pull did not say the daemon pulls it later:\n%s", out.String())
	}

	runner.calls = nil
	w.opts.Runner = runner
	w.cfg.Image, w.cfg.ImageTag = "", ""
	w.pullAgentImages(context.Background())
	if len(runner.calls) != 0 {
		t.Errorf("a stack built from a checkout pulled %v", runner.calls)
	}
}
