package sandbox

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestAppPortsFromEnv(t *testing.T) {
	cases := map[string][]int{
		"":                     DefaultExposedPorts,
		"4200":                 {4200},
		"3000, 4200;3000 8080": {3000, 4200, 8080},
		"3000,abc,0,70000":     {3000},
		"abc,-1":               DefaultExposedPorts,
	}
	for in, want := range cases {
		got := AppPortsFromEnv(func(k string) string {
			if k == EnvSandboxPorts {
				return in
			}
			return ""
		})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("AppPortsFromEnv(%q) = %v, want %v", in, got, want)
		}
	}
	got := AppPortsFromEnv(func(string) string { return "" })
	got[0] = 1
	if DefaultExposedPorts[0] != 3000 {
		t.Fatal("AppPortsFromEnv returned the shared defaults")
	}
}

func TestManagerPassesAppPortsToRuntime(t *testing.T) {
	m, _, _, rt := newFakeManager(t)
	ctx := context.Background()
	if _, err := m.Acquire(ctx, "task-ports-1", "in_progress", AcquireDockerOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := m.AppPorts(); !reflect.DeepEqual(got, DefaultExposedPorts) {
		t.Errorf("default AppPorts = %v", got)
	}
	m2 := NewManager(m.store, m.settings, rt, ManagerConfig{AppPorts: []int{4200}})
	if _, err := m2.Acquire(ctx, "task-ports-2", "in_progress", AcquireDockerOpts{}); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.exposed) != 2 || !reflect.DeepEqual(rt.exposed[0], DefaultExposedPorts) || !reflect.DeepEqual(rt.exposed[1], []int{4200}) {
		t.Errorf("ExposePorts per Create = %v, want [defaults [4200]]", rt.exposed)
	}
	if got := appPorts(CreateOpts{}); !reflect.DeepEqual(got, DefaultExposedPorts) {
		t.Errorf("appPorts without a list = %v, want the defaults", got)
	}
}

func TestPreviewToolDescribesPorts(t *testing.T) {
	for _, def := range mcpToolDefs([]int{3000, 4200}) {
		if def["name"] != "preview_url" {
			continue
		}
		if desc, _ := def["description"].(string); !strings.Contains(desc, "3000, 4200") {
			t.Errorf("preview_url description = %q, want the port list", desc)
		}
		return
	}
	t.Fatal("preview_url tool missing")
}
