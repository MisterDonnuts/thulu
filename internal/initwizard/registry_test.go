package initwizard

import (
	"strings"
	"testing"
)

func TestImageRegistryOverride(t *testing.T) {
	t.Setenv(ImageRegistryEnv, "registry.corp.example/octopus/")
	if got := ReleaseImage("1.4.0"); got != "registry.corp.example/octopus/octopus:1.4.0" {
		t.Errorf("ReleaseImage = %q", got)
	}
	if got := ReleaseImageFor("SomeFork/octopus", "v2.0.0"); got != "registry.corp.example/octopus/octopus:2.0.0" {
		t.Errorf("ReleaseImageFor = %q: the override must beat the repository's own registry", got)
	}
}

func TestImageRegistryDefaultFollowsTheRepoOwner(t *testing.T) {
	t.Setenv(ImageRegistryEnv, "")
	if got := ImageRegistry("SomeFork/octopus"); got != "ghcr.io/somefork" {
		t.Errorf("ImageRegistry = %q, want the lowercased owner on ghcr.io", got)
	}
}

func TestReleaseRepoOverride(t *testing.T) {
	t.Setenv(ReleaseRepoEnv, "")
	if ReleaseRepo() != DefaultRepo {
		t.Errorf("ReleaseRepo = %q without an override", ReleaseRepo())
	}
	t.Setenv(ReleaseRepoEnv, "acme/octopus-mirror")
	if ReleaseRepo() != "acme/octopus-mirror" {
		t.Errorf("ReleaseRepo = %q", ReleaseRepo())
	}
}

func TestSplitDaemonImage(t *testing.T) {
	cases := []struct {
		ref, registry, tag string
		ok                 bool
	}{
		{"ghcr.io/misterdonnuts/octopus:1.4.0", "ghcr.io/misterdonnuts", "1.4.0", true},
		{"registry.corp:5000/mirror/octopus:1.4.0", "registry.corp:5000/mirror", "1.4.0", true},
		{"registry.corp/custom-daemon:1.4.0", "", "", false},
		{"ghcr.io/misterdonnuts/octopus@sha256:abc", "", "", false},
		{"octopus:latest", "", "", false},
	}
	for _, c := range cases {
		reg, tag, ok := splitDaemonImage(c.ref)
		if reg != c.registry || tag != c.tag || ok != c.ok {
			t.Errorf("splitDaemonImage(%q) = %q, %q, %v", c.ref, reg, tag, ok)
		}
	}
}

func TestInterviewMirroredImageCarriesTheRegistryToTheMCPImages(t *testing.T) {
	t.Setenv(ImageRegistryEnv, "")
	cfg, _ := runInterview(t, &Interviewer{NoSource: true, BinaryVersion: "1.4.0"},
		"2\n2\nregistry.corp/mirror/octopus:1.4.0\n3\n\n\n\ns\n\n\n\n\n")
	if cfg.ImageRegistry != "registry.corp/mirror" || cfg.ImageTag != "1.4.0" {
		t.Fatalf("registry=%q tag=%q", cfg.ImageRegistry, cfg.ImageTag)
	}
	f := &EnvFile{}
	cfg.JWTSecret = "x"
	cfg.ApplyToEnv(f)
	if f.Get(ImageRegistryEnv) != "registry.corp/mirror" || f.Get("OCTOPUS_VERSION") != "1.4.0" {
		t.Errorf("deploy/.env does not pin the MCP images:\n%s", f.Render())
	}
}

func TestInterviewDefaultImageLeavesTheRegistryImplicit(t *testing.T) {
	t.Setenv(ImageRegistryEnv, "")
	cfg, _ := noSourceTranscript(t, "2\n2\n\n3\n\n\n\ns\n\n\n\n\n")
	if cfg.ImageRegistry != "" {
		t.Errorf("registry = %q for the default image", cfg.ImageRegistry)
	}
	if cfg.ImageTag != "1.4.0" {
		t.Errorf("tag = %q, want this binary's release", cfg.ImageTag)
	}
}

func TestInterviewKeepsTheLegacyPostgresPassword(t *testing.T) {
	existing := &EnvFile{lines: []string{
		"OCTOPUS_DATABASE_URL=postgres://octopus:octopus@pg:5432/octopus?sslmode=disable",
	}}
	cfg, out := transcript(t, "2\n2\n\n3\n\n\n\ns\n\n\n\n\n", existing)
	if cfg.PGPassword != legacyPGPassword {
		t.Errorf("password = %q, want the one the volume was initialized with", cfg.PGPassword)
	}
	if !strings.Contains(out, "ALTER USER") {
		t.Errorf("no rotation hint for the old shared password:\n%s", out)
	}
}

func TestInterviewKeepsAnExistingPostgresPassword(t *testing.T) {
	existing := &EnvFile{lines: []string{"OCTOPUS_PG_PASSWORD=s3cr3t/with:chars"}}
	cfg, _ := transcript(t, "2\n2\n\n3\n\n\n\ns\n\n\n\n\n", existing)
	if cfg.PGPassword != "s3cr3t/with:chars" {
		t.Fatalf("password = %q", cfg.PGPassword)
	}
	if bundledPasswordFromDSN(cfg.DatabaseURL) != "s3cr3t/with:chars" {
		t.Errorf("DSN %q does not round-trip the password", cfg.DatabaseURL)
	}
}

func TestBundledPasswordIgnoresAnExternalDatabase(t *testing.T) {
	if got := bundledPasswordFromDSN("postgres://app:hunter2@db.corp:5432/octopus"); got != "" {
		t.Errorf("reused an external database's password: %q", got)
	}
}
