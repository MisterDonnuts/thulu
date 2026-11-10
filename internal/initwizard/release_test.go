package initwizard

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseImage(t *testing.T) {
	cases := map[string]string{
		"1.2.3":  "ghcr.io/misterdonnuts/octopus:1.2.3",
		"v1.2.3": "ghcr.io/misterdonnuts/octopus:1.2.3",
		"dev":    "ghcr.io/misterdonnuts/octopus:latest",
		"":       "ghcr.io/misterdonnuts/octopus:latest",
	}
	for in, want := range cases {
		if got := ReleaseImage(in); got != want {
			t.Errorf("ReleaseImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComposeArgsSkipsBuildForAReleaseImage(t *testing.T) {
	release := &Config{ComposeFile: standaloneComposeFile, Image: "ghcr.io/misterdonnuts/octopus:1.0.0"}
	if args := strings.Join(ComposeArgs(release, nil), " "); strings.Contains(args, "--build") {
		t.Errorf("release install would build: docker %s", args)
	}
	checkout := &Config{ComposeFile: devComposeFile}
	if args := strings.Join(ComposeArgs(checkout, nil), " "); !strings.Contains(args, "--build") {
		t.Errorf("checkout install would not rebuild: docker %s", args)
	}
}

func TestApplyToEnvKeepsAnImageItDidNotChoose(t *testing.T) {
	f := &EnvFile{}
	f.Set("OCTOPUS_IMAGE", "ghcr.io/misterdonnuts/octopus:1.0.0")
	(&Config{Mode: ModeDocker, ComposeFile: prodComposeFile}).ApplyToEnv(f)
	if got := f.Get("OCTOPUS_IMAGE"); got != "ghcr.io/misterdonnuts/octopus:1.0.0" {
		t.Errorf("OCTOPUS_IMAGE = %q after a rerun that never asked about it", got)
	}
}

func noSourceTranscript(t *testing.T, answers string) (*Config, string) {
	t.Helper()
	return runInterview(t, &Interviewer{NoSource: true, BinaryVersion: "1.4.0"}, answers)
}

func TestInterviewWithoutSourcePicksTheStandaloneStack(t *testing.T) {
	cfg, _ := noSourceTranscript(t, "2\n2\n\n3\n\n\n\ns\n\n\n\n\n")

	if cfg.ComposeFile != standaloneComposeFile {
		t.Fatalf("compose = %q, want the standalone stack — nothing here can build the other two", cfg.ComposeFile)
	}
	if cfg.Image != "ghcr.io/misterdonnuts/octopus:1.4.0" {
		t.Errorf("image = %q, want the tag matching this binary", cfg.Image)
	}
	if cfg.Identity != IdentityNone {
		t.Fatalf("identity = %q, want none", cfg.Identity)
	}
	if cfg.Bind != "127.0.0.1" {
		t.Errorf("bind = %q — with auth disabled the bind address is the whole access control", cfg.Bind)
	}
	if strings.Contains(cfg.PublicOrigin, "4321") {
		t.Errorf("origins = %q: only the dev stack serves the dashboard from Vite", cfg.PublicOrigin)
	}
	if !strings.Contains(cfg.PublicOrigin, "localhost:8585") {
		t.Errorf("origins = %q: the daemon answers on 8585, and a bare origin never matches", cfg.PublicOrigin)
	}
}

func TestInterviewWithoutSourceWarnsWhenPublishedOffLoopback(t *testing.T) {
	var out bytes.Buffer
	cfg := &Config{}
	iv := &Interviewer{
		P: NewTestPrompter(strings.NewReader(
			"2\n2\n\n1\nhttps://id.invalid\ny\nclient-id\nclient-secret\n\nacme.com\n\ns\n\n\n0.0.0.0\n\ns\n\n\n\n\n\n"), &out),
		Cfg:      cfg,
		Ctx:      context.Background(),
		Detect:   func() {},
		NoSource: true,
	}
	if err := iv.Run(); err != nil {
		t.Fatalf("interview: %v\n%s", err, out.String())
	}
	if cfg.Bind != "0.0.0.0" {
		t.Fatalf("bind = %q, want the address that was typed", cfg.Bind)
	}
	if cfg.OIDC.AllowedDomains != "acme.com" {
		t.Errorf("allowed domains = %q, want the typed list", cfg.OIDC.AllowedDomains)
	}
	if cfg.TrustedProxies != "" {
		t.Errorf("trusted proxies = %q, want none", cfg.TrustedProxies)
	}
	if !strings.Contains(strings.Join(deferredNames(cfg.Deferred), ","), "TLS") {
		t.Errorf("publishing plain HTTP off loopback was not recorded as undone: %v", cfg.Deferred)
	}
}

func TestNoLoginOverrideStaysOnTheDevStack(t *testing.T) {
	w := &run{cfg: &Config{
		Mode: ModeDocker, Identity: IdentityNone,
		ComposeFile: standaloneComposeFile, Bind: "127.0.0.1",
		Image: "ghcr.io/misterdonnuts/octopus:1.0.0",
	}}
	if got := w.overrides(); len(got) != 0 {
		t.Errorf("standalone install layers %v, and neither service in it exists there", got)
	}
	w.cfg.ComposeFile = devComposeFile
	if got := w.overrides(); len(got) != 1 {
		t.Errorf("dev stack lost its loopback override: %v", got)
	}
}

func TestAlignDockerGroupWritesTheSocketGroup(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	original := socketGID
	socketGID = func(context.Context, string) string { return "991" }
	t.Cleanup(func() { socketGID = original })

	w := &run{
		opts:    Options{Root: root},
		out:     io.Discard,
		env:     &EnvFile{},
		envPath: filepath.Join(root, "deploy", ".env"),
		cfg: &Config{
			Mode: ModeDocker, ComposeFile: standaloneComposeFile,
			Image: "ghcr.io/misterdonnuts/octopus:1.0.0",
		},
	}
	if err := w.alignDockerGroup(context.Background()); err != nil {
		t.Fatalf("alignDockerGroup: %v", err)
	}
	body, err := os.ReadFile(w.envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "OCTOPUS_DOCKER_GID=991") {
		t.Errorf("deploy/.env carries no socket group:\n%s", body)
	}
}

func TestAlignDockerGroupRecordsAFailedProbe(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	original := socketGID
	socketGID = func(context.Context, string) string { return "" }
	t.Cleanup(func() { socketGID = original })

	w := &run{
		opts:    Options{Root: root},
		out:     io.Discard,
		env:     &EnvFile{},
		envPath: filepath.Join(root, "deploy", ".env"),
		cfg: &Config{
			Mode: ModeDocker, ComposeFile: standaloneComposeFile,
			Image: "ghcr.io/misterdonnuts/octopus:1.0.0",
		},
	}
	if err := w.alignDockerGroup(context.Background()); err != nil {
		t.Fatalf("alignDockerGroup: %v", err)
	}
	if !strings.Contains(strings.Join(deferredNames(w.cfg.Deferred), ","), "Docker socket") {
		t.Errorf("a probe that came back empty left no trail: %v", w.cfg.Deferred)
	}
}

func TestProbeImagePerStack(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		image string
	}{
		{"standalone runs the release image", Config{
			Mode: ModeDocker, ComposeFile: standaloneComposeFile,
			Image: "ghcr.io/misterdonnuts/octopus:1.0.0",
		}, "ghcr.io/misterdonnuts/octopus:1.0.0"},
		{"self-hosted asks the image it already pulls", Config{
			Mode: ModeDocker, ComposeFile: prodComposeFile,
		}, bundledPostgresImage},
		{"dev stack needs no group", Config{
			Mode: ModeDocker, ComposeFile: devComposeFile,
		}, ""},
		{"kubernetes mounts no socket at all", Config{
			Mode: ModeKubernetes,
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.cfg
			if got := (&run{cfg: &cfg}).probeImage(); got != c.image {
				t.Errorf("probeImage = %q, want %q", got, c.image)
			}
		})
	}
}
