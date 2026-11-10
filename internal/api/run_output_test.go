package api

import (
	"path/filepath"
	"testing"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"

	"github.com/MisterDonnuts/thulu/internal/agent"
)

func TestRunOutputDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data", "runs")
	inst := "developer-task-0123abcd-in_progress-1727500000000"
	cases := []struct {
		name     string
		runsRoot string
		instance string
		want     string
	}{
		{"stage run", root, inst, filepath.Join(root, inst)},
		{"mention run", root, "qa-mention-0123abcd-c1-1727500000000", filepath.Join(root, "qa-mention-0123abcd-c1-1727500000000")},
		{"root is cleaned", root + string(filepath.Separator), inst, filepath.Join(root, inst)},
		{"no root", "", inst, ""},
		{"relative root", filepath.Join("data", "runs"), inst, ""},
		{"no instance", root, "", ""},
		{"dot", root, ".", ""},
		{"dot dot", root, "..", ""},
		{"escapes the root", root, "../etc", ""},
		{"nested", root, "a/b", ""},
		{"backslash", root, `a\b`, ""},
		{"hidden", root, ".session_id", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runOutputDir(tc.runsRoot, tc.instance); got != tc.want {
				t.Fatalf("runOutputDir(%q, %q) = %q, want %q", tc.runsRoot, tc.instance, got, tc.want)
			}
		})
	}
}

func TestServerRunOutputDirIgnoresTheAgentDefinition(t *testing.T) {
	root := t.TempDir()
	srv := &Server{runsDir: root, agentsDir: filepath.Join(root, "agents")}
	if got, want := srv.runOutputDirOf("inst-1"), filepath.Join(root, "inst-1"); got != want {
		t.Fatalf("runOutputDirOf = %q, want %q", got, want)
	}
	if got := (&Server{}).runOutputDirOf("inst-1"); got != "" {
		t.Fatalf("runOutputDirOf with no runs dir = %q, want empty", got)
	}
}

func TestContainerInstanceID(t *testing.T) {
	named := func(name string, cfg *container.Config) dtypes.ContainerJSON {
		return dtypes.ContainerJSON{
			ContainerJSONBase: &dtypes.ContainerJSONBase{Name: name},
			Config:            cfg,
		}
	}
	cases := []struct {
		name string
		info dtypes.ContainerJSON
		want string
	}{
		{"label wins", named("/by-name", &container.Config{
			Labels: map[string]string{agent.LabelInstance: "by-label"},
			Env:    []string{"INSTANCE_ID=by-env"},
		}), "by-label"},
		{"env when the label is missing", named("/by-name", &container.Config{
			Env: []string{"PATH=/bin", "INSTANCE_ID=by-env"},
		}), "by-env"},
		{"name when both are missing", named("/by-name", &container.Config{}), "by-name"},
		{"no config", named("/by-name", nil), "by-name"},
		{"nothing at all", dtypes.ContainerJSON{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containerInstanceID(tc.info); got != tc.want {
				t.Fatalf("containerInstanceID = %q, want %q", got, tc.want)
			}
		})
	}
}
