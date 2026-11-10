package deploy

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
)

func publishedImageEnv() []string {
	keys := []string{"OCTOPUS_SANDBOX_IMAGE"}
	for _, e := range agentimage.Engines {
		keys = append(keys, agentimage.Env(e))
	}
	return keys
}

func TestCaddyStackMatchesTheSelfHostedOne(t *testing.T) {
	prod := composeTree(t, "docker-compose.yml")
	caddy := composeTree(t, "docker-compose.caddy.yml")

	prodServices := prod["services"].(map[string]any)
	caddyServices := caddy["services"].(map[string]any)
	for name, raw := range prodServices {
		want := raw.(map[string]any)
		got, ok := caddyServices[name].(map[string]any)
		if !ok {
			t.Errorf("service %s is missing from docker-compose.caddy.yml", name)
			continue
		}
		if _, built := got["build"]; built {
			t.Errorf("service %s builds in docker-compose.caddy.yml; that stack has no source to build", name)
		}
		_, wasBuilt := want["build"]
		delete(want, "build")
		if wasBuilt {
			image, _ := got["image"].(string)
			if !strings.Contains(image, "OCTOPUS_VERSION:?") {
				t.Errorf("service %s image %q does not require OCTOPUS_VERSION", name, image)
			}
			delete(want, "image")
			delete(got, "image")
		}
		if name == "octopus" {
			env, _ := got["environment"].(map[string]any)
			for _, key := range publishedImageEnv() {
				if v, _ := env[key].(string); !strings.Contains(v, "OCTOPUS_VERSION:?") {
					t.Errorf("octopus %s = %q does not require OCTOPUS_VERSION", key, v)
				}
				delete(env, key)
			}
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("service %s differs between the two stacks:\nprod:  %v\ncaddy: %v", name, want, got)
		}
	}
	for name := range caddyServices {
		if _, ok := prodServices[name]; !ok {
			t.Errorf("service %s is only in docker-compose.caddy.yml", name)
		}
	}
	for _, key := range []string{"volumes", "networks", "x-logging", "x-mcp-server"} {
		if !reflect.DeepEqual(prod[key], caddy[key]) {
			t.Errorf("%s differs between the two stacks", key)
		}
	}
}

func composeTree(t *testing.T, name string) map[string]any {
	t.Helper()
	body, err := Files.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var tree map[string]any
	if err := yaml.Unmarshal(body, &tree); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return tree
}

func TestPublishedStacksNameEveryAgentImage(t *testing.T) {
	for _, file := range []string{"docker-compose.caddy.yml", "docker-compose.standalone.yml"} {
		svc, _ := composeTree(t, file)["services"].(map[string]any)
		octopus, _ := svc["octopus"].(map[string]any)
		env, _ := octopus["environment"].(map[string]any)
		for _, key := range publishedImageEnv() {
			if v, _ := env[key].(string); !strings.Contains(v, "${"+key+":-") || !strings.Contains(v, "OCTOPUS_VERSION:?") {
				t.Errorf("%s: octopus %s = %q, want the .env value or the published image at OCTOPUS_VERSION", file, key, v)
			}
		}
	}
}
