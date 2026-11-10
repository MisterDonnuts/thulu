package initwizard

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/deploy"
)

var composeDefault = regexp.MustCompile(`\$\{[A-Z0-9_]+:-([^}]*)\}`)

func TestEveryDockerStackPublishesTheDaemonOnLoopback(t *testing.T) {
	w := &run{cfg: &Config{Mode: ModeDocker}}
	u, err := url.Parse(w.daemonURL())
	if err != nil {
		t.Fatalf("daemonURL %q: %v", w.daemonURL(), err)
	}
	if u.Hostname() != "localhost" {
		t.Fatalf("daemonURL %q: the compose stacks publish on loopback, not on %s", w.daemonURL(), u.Hostname())
	}
	for _, file := range []string{prodComposeFile, caddyComposeFile, devComposeFile, standaloneComposeFile} {
		if !publishesOnLoopback(t, file, u.Port()) {
			t.Errorf("%s publishes nothing on 127.0.0.1:%s, where the wizard and the updater wait for the daemon", file, u.Port())
		}
	}
}

func publishesOnLoopback(t *testing.T, file, port string) bool {
	t.Helper()
	body, err := deploy.Files.ReadFile(strings.TrimPrefix(file, "deploy/"))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var tree struct {
		Services map[string]struct {
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &tree); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, svc := range tree.Services {
		for _, mapping := range svc.Ports {
			parts := strings.Split(composeDefault.ReplaceAllString(mapping, "$1"), ":")
			if len(parts) == 3 && parts[0] == "127.0.0.1" && parts[1] == port {
				return true
			}
		}
	}
	return false
}

func TestDashboardURLBehindCaddyIsTheDomain(t *testing.T) {
	for _, c := range []struct {
		file, domain, want string
	}{
		{caddyComposeFile, "thulu.acme.com", "https://thulu.acme.com"},
		{prodComposeFile, "thulu.acme.com", "https://thulu.acme.com"},
		{caddyComposeFile, "localhost", "http://localhost:8585"},
		{standaloneComposeFile, "thulu.acme.com", "http://localhost:8585"},
	} {
		w := &run{cfg: &Config{Mode: ModeDocker, ComposeFile: c.file, Domain: c.domain}}
		if got := w.dashboardURL(); got != c.want {
			t.Errorf("%s on %s: dashboard = %q, want %q", c.file, c.domain, got, c.want)
		}
	}
}
