package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"

	"github.com/MisterDonnuts/thulu/internal/dockergc/dockergctest"
	"github.com/MisterDonnuts/thulu/internal/runner/service"
)

func install(t *testing.T) string {
	t.Helper()
	t.Setenv("OCTOPUS_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OCTOPUS_RUNNER_CONFIG", "")
	stubRunnerService(t, service.Status{}, nil)
	t.Setenv("OCTOPUS_URL", "http://127.0.0.1:1")
	for _, k := range []string{"OCTOPUS_IMAGE", "OCTOPUS_AGENT_IMAGE", "OCTOPUS_VERSION", "OCTOPUS_IMAGE_REGISTRY",
		"OCTOPUS_BACKUP_KEEP", "OCTOPUS_ROLLBACK_IMAGE", "OCTOPUS_ROLLBACK_UNTIL", "COMPOSE_PROJECT_NAME"} {
		t.Setenv(k, "")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("deploy/docker-compose.yml", "services: {}\n")
	write("deploy/docker-compose.standalone.yml", "services: {}\n")
	write("deploy/.octopus-init.json", `{"mode":"docker","compose_file":"deploy/docker-compose.standalone.yml"}`)
	write("deploy/.env", "OCTOPUS_IMAGE=ghcr.io/misterdonnuts/octopus:1.1.0\nOCTOPUS_VERSION=1.1.0\n"+
		"OCTOPUS_AGENT_IMAGE=octopus-agent:local\nOCTOPUS_JWT_SECRET=s3cret\n"+
		"OCTOPUS_ROLLBACK_IMAGE=ghcr.io/misterdonnuts/octopus:1.0.0\nOCTOPUS_ROLLBACK_UNTIL=2020-01-08T12:00:00Z\n")
	for i := 1; i <= 7; i++ {
		ext := ".sql"
		if i%2 == 0 {
			ext = ".tar.gz"
		}
		write(fmt.Sprintf("backups/octopus_2026090%d_120000%s", i, ext), "dump")
	}
	pinned := filepath.Join(root, "backups", "octopus_20260901_120000.sql")
	write("deploy/.octopus-update.json", `{"updated_at":"2020-01-01T12:00:00Z","from":{"image":"ghcr.io/misterdonnuts/octopus:1.0.0"},`+
		`"to":{"image":"ghcr.io/misterdonnuts/octopus:1.1.0"},"backup_path":"`+pinned+`"}`)
	return root
}

func fakeHost(root string) *dockergctest.Docker {
	stack := map[string]string{
		"com.docker.compose.project":             "deploy",
		"com.docker.compose.project.working_dir": filepath.Join(root, "deploy"),
	}
	d := &dockergctest.Docker{}
	d.Images = append(d.Images,
		dockergctest.Image("sha256:new", 190e6, "ghcr.io/misterdonnuts/octopus:1.1.0"),
		dockergctest.Image("sha256:old", 190e6, "ghcr.io/misterdonnuts/octopus:1.0.0"),
		dockergctest.Image("sha256:older", 190e6, "ghcr.io/misterdonnuts/octopus:0.9.0"),
		dockergctest.Image("sha256:agent", 1400e6, "octopus-agent:local"),
		dockergctest.Image("sha256:pg", 390e6, "postgres:16-alpine"),
		dockergctest.Image("sha256:other", 900e6, "shopapp:latest"),
	)
	d.Containers = []types.Container{
		dockergctest.Container("c1", "deploy-octopus-1", "ghcr.io/misterdonnuts/octopus:1.1.0", "sha256:new", "running", stack),
		dockergctest.Container("c2", "deploy-pg-1", "postgres:16-alpine", "sha256:pg", "running", stack),
		dockergctest.Container("c3", "shopapp-1", "shopapp", "sha256:other", "exited",
			map[string]string{"com.docker.compose.project": "shopapp"}),
	}
	d.Containers[0].Mounts = []types.MountPoint{{Name: "deploy_octopus-data"}}
	d.Containers[0].NetworkSettings = &types.SummaryNetworkSettings{
		Networks: map[string]*network.EndpointSettings{"octopus-agents": {NetworkID: "n1"}},
	}
	d.Containers[1].Mounts = []types.MountPoint{{Name: "deploy_octopus-pg"}}
	d.Volumes = []*volume.Volume{
		{Name: "deploy_octopus-data", Labels: map[string]string{"com.docker.compose.project": "deploy", "com.docker.compose.volume": "octopus-data"}},
		{Name: "deploy_octopus-pg", Labels: map[string]string{"com.docker.compose.project": "deploy", "com.docker.compose.volume": "octopus-pg"}},
		{Name: "shopapp-postgres-data", Labels: map[string]string{"com.docker.compose.project": "shopapp"}},
	}
	d.Networks = []network.Summary{
		{ID: "n1", Name: "octopus-agents", Labels: map[string]string{"com.docker.compose.project": "deploy"}},
		{ID: "n2", Name: "shopapp-network", Labels: map[string]string{"com.docker.compose.project": "shopapp"}},
	}
	return d
}

func TestGCDryRunListsAndRemovesNothing(t *testing.T) {
	root := install(t)
	d := fakeHost(root)
	var out bytes.Buffer
	if err := runGC(context.Background(), &out, d, GCOptions{Root: root}); err != nil {
		t.Fatalf("gc: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Projects  deploy",
		"Rollback  ghcr.io/misterdonnuts/octopus:1.0.0 no longer kept: the window ended 2020-01-08",
		"ghcr.io/misterdonnuts/octopus:0.9.0",
		"Backups (1,",
		"octopus_20260902_120000.tar.gz",
		"Run `thulu gc -apply`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	for _, never := range []string{"shopapp", "postgres", "octopus_20260901_120000.sql"} {
		if strings.Contains(sectionBefore(text, "Keeping"), never) {
			t.Errorf("dry run lists %q for removal:\n%s", never, text)
		}
	}
	if len(d.Removed) != 0 {
		t.Errorf("a dry run removed %v", d.Removed)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "backups")); len(entries) != 7 {
		t.Errorf("a dry run removed backups: %d left", len(entries))
	}
}

func TestGCApplyRemovesOnlyThePlan(t *testing.T) {
	root := install(t)
	d := fakeHost(root)
	var out bytes.Buffer
	if err := runGC(context.Background(), &out, d, GCOptions{Root: root, Apply: true}); err != nil {
		t.Fatalf("gc: %v\n%s", err, out.String())
	}
	want := []string{"image ghcr.io/misterdonnuts/octopus:0.9.0", "image ghcr.io/misterdonnuts/octopus:1.0.0"}
	if strings.Join(d.Removed, ",") != strings.Join(want, ",") {
		t.Errorf("removed %v, want %v", d.Removed, want)
	}
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) != 6 || !strings.Contains(strings.Join(left, ","), "octopus_20260901_120000.sql") {
		t.Errorf("backups left: %v", left)
	}
}

func TestUninstallDryRunKeepsDataAndConfig(t *testing.T) {
	root := install(t)
	d := fakeHost(root)
	runner := &recordingRunner{}
	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out, d, runner, UninstallOptions{Root: root}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"docker compose -f deploy/docker-compose.standalone.yml down --remove-orphans",
		"deploy-octopus-1", "deploy-pg-1", "ghcr.io/misterdonnuts/octopus:1.1.0", "octopus-agent:local",
		filepath.Join(root, "deploy", ".octopus-init.json"),
		"Kept without -purge-data",
		"Run `thulu uninstall -yes`",
		"This binary stays",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "deploy_octopus-pg") || strings.Contains(text, "shopapp") {
		t.Errorf("dry run lists data volumes or foreign objects:\n%s", text)
	}
	if len(d.Removed) != 0 || len(runner.calls) != 0 {
		t.Errorf("a dry run acted: removed %v, ran %v", d.Removed, runner.calls)
	}
}

func TestUninstallYesPurgeRemovesEverythingOctopus(t *testing.T) {
	root := install(t)
	d := fakeHost(root)
	runner := &recordingRunner{}
	var out bytes.Buffer
	err := runUninstall(context.Background(), &out, d, runner, UninstallOptions{Root: root, Yes: true, PurgeData: true})
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "!! -purge-data deletes") {
		t.Error("no warning before purging data")
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "down --remove-orphans") {
		t.Errorf("ran %v, want compose down", runner.calls)
	}
	removed := strings.Join(d.Removed, "\n")
	for _, want := range []string{"container c1", "container c2", "image ghcr.io/misterdonnuts/octopus:1.1.0",
		"image octopus-agent:local", "volume deploy_octopus-pg", "volume deploy_octopus-data", "network octopus-agents"} {
		if !strings.Contains(removed, want) {
			t.Errorf("uninstall left %s; removed:\n%s", want, removed)
		}
	}
	for _, never := range []string{"shopapp", "postgres:16-alpine", "c3"} {
		if strings.Contains(removed, never) {
			t.Errorf("uninstall removed foreign %s", never)
		}
	}
	for _, rel := range []string{"deploy/.env", "backups", "deploy/.octopus-init.json", "deploy/.octopus-update.json"} {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr == nil {
			t.Errorf("%s survived -purge-data", rel)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "deploy", "docker-compose.yml")); statErr != nil {
		t.Error("uninstall deleted a file of the checkout itself")
	}
}

func TestUninstallHelmOnlyPrintsCommands(t *testing.T) {
	root := install(t)
	if err := os.WriteFile(filepath.Join(root, "deploy", ".octopus-init.json"),
		[]byte(`{"mode":"kubernetes","release":"octopus","namespace":"octopus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := fakeHost(root)
	runner := &recordingRunner{}
	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out, d, runner, UninstallOptions{Root: root, Yes: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "helm uninstall octopus --namespace octopus") ||
		!strings.Contains(out.String(), "kubectl delete namespace octopus octopus-sandboxes") {
		t.Errorf("output:\n%s", out.String())
	}
	if len(d.Removed) != 0 || len(runner.calls) != 0 {
		t.Errorf("a Kubernetes uninstall acted on this host: %v %v", d.Removed, runner.calls)
	}
}

type recordingRunner struct{ calls []string }

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil
}

func sectionBefore(text, marker string) string {
	if i := strings.Index(text, marker); i >= 0 {
		return text[:i]
	}
	return text
}
