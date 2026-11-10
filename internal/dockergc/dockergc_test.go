package dockergc_test

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"

	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/dockergc/dockergctest"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const octopusSource = "https://github.com/MisterDonnuts/thulu"

func host() *dockergctest.Docker {
	daemonLabels := map[string]string{"org.opencontainers.image.source": octopusSource, "org.opencontainers.image.title": "octopus"}
	d := &dockergctest.Docker{Finished: map[string]string{}}
	add := func(id string, size int64, labels map[string]string, parent string, tags ...string) {
		i := dockergctest.Image(id, size, tags...)
		i.Labels, i.ParentID = labels, parent
		d.Images = append(d.Images, i)
	}
	add("sha256:dev-a", 189e6, daemonLabels, "", "ghcr.io/misterdonnuts/octopus:dev-a", "ghcr.io/misterdonnuts/octopus:dev-c")
	add("sha256:dev-b", 189e6, daemonLabels, "", "ghcr.io/misterdonnuts/octopus:dev-b", "ghcr.io/misterdonnuts/octopus:dev-d")
	add("sha256:prev", 188e6, daemonLabels, "", "ghcr.io/misterdonnuts/octopus:dev-a-prev")
	add("sha256:v010", 187e6, daemonLabels, "", "ghcr.io/misterdonnuts/octopus:0.1.0")
	add("sha256:daemonlocal", 188e6, daemonLabels, "", "octopus-daemon:latest")
	add("sha256:devapi", 770e6, nil, "", "deploy-api:latest")
	add("sha256:agentlocal", 1420e6, nil, "", "octopus-agent:local")
	add("sha256:agentold", 1400e6, nil, "", "ghcr.io/misterdonnuts/octopus-agent:0.1.0")
	add("sha256:dockeragent", 1400e6, nil, "", "octopus-docker-agent:latest")
	add("sha256:mcpcloud", 506e6, nil, "", "thulu-cloud:latest")
	add("sha256:failedbuild", 12e6, daemonLabels, "sha256:alpine", "<none>:<none>")
	add("sha256:intermediate", 9e6, nil, "sha256:failedbuild", "<none>:<none>")
	add("sha256:alpine", 13e6, nil, "", "alpine:3.24")
	add("sha256:billingauth", 982e6, nil, "", "billing-auth:local")
	add("sha256:mongo", 1120e6, nil, "", "mongo:7")
	add("sha256:foreigndangling", 978e6, map[string]string{"com.docker.compose.image.builder": "classic"}, "", "<none>:<none>")
	add("sha256:otheroctopus", 50e6, nil, "", "acme/octopus:1.0")

	deploy := map[string]string{
		"com.docker.compose.project":             "deploy",
		"com.docker.compose.project.working_dir": "/opt/app/deploy",
		"com.docker.compose.service":             "octopus",
	}
	d.Containers = []types.Container{
		dockergctest.Container("c-daemon", "deploy-octopus-1", "ghcr.io/misterdonnuts/octopus:dev-a", "sha256:dev-a", "running", deploy),
		dockergctest.Container("c-mcp", "thulu-cloud", "thulu-cloud", "sha256:mcpcloud", "exited",
			map[string]string{"com.docker.compose.project": "tools", "com.docker.compose.project.working_dir": "/opt/app/tools"}),
		dockergctest.Container("c-agent-old", "agent-old", "octopus-agent:local", "sha256:agentlocal", "exited",
			map[string]string{"thulu.agent": "true"}),
		dockergctest.Container("c-agent-fresh", "agent-fresh", "octopus-agent:local", "sha256:agentlocal", "exited",
			map[string]string{"thulu.agent": "true"}),
		dockergctest.Container("c-sandbox-up", "sandbox-up", "octopus-runtime:latest", "sha256:runtime", "running",
			map[string]string{"thulu.sandbox": "true"}),
		dockergctest.Container("c-build", "charming_tesla", "sha256:failedbuild", "sha256:failedbuild", "exited", nil),
		dockergctest.Container("c-billing", "billing-auth-app", "billing-auth:local", "sha256:billingauth", "running",
			map[string]string{"com.docker.compose.project": "billing-auth"}),
		dockergctest.Container("c-mongo", "e2e-mongodb", "mongo:7", "sha256:mongo", "exited",
			map[string]string{"com.docker.compose.project": "billing-e2e"}),
		dockergctest.Container("c-foreign-build", "admiring_ellis", "sha256:foreigndangling", "sha256:foreigndangling", "exited", nil),
	}
	d.Finished["c-agent-old"] = now.Add(-5 * time.Hour).Format(time.RFC3339Nano)
	d.Finished["c-agent-fresh"] = now.Add(-10 * time.Minute).Format(time.RFC3339Nano)

	vol := func(name, project, role string) *volume.Volume {
		return &volume.Volume{Name: name, Labels: map[string]string{
			"com.docker.compose.project": project, "com.docker.compose.volume": role,
		}}
	}
	d.Volumes = []*volume.Volume{
		vol("deploy_octopus-data", "deploy", "octopus-data"),
		vol("deploy_octopus-pg", "deploy", "octopus-pg"),
		vol("deploy_go-mod-cache", "deploy", "go-mod-cache"),
		vol("shop_pgdata", "shop", "pgdata"),
	}
	d.Containers[0].Mounts = []types.MountPoint{{Name: "deploy_octopus-data"}}
	d.Networks = []network.Summary{
		{ID: "n-net", Name: "deploy_octopus-net", Labels: map[string]string{"com.docker.compose.project": "deploy"}},
		{ID: "n-edge", Name: "deploy_edge", Labels: map[string]string{"com.docker.compose.project": "deploy"}},
		{ID: "n-agents", Name: "octopus-agents", Labels: map[string]string{"thulu.dev/managed-by": "octopus"}},
		{ID: "n-other", Name: "shop_default", Labels: map[string]string{"com.docker.compose.project": "shop"}},
		{ID: "n-bridge", Name: "bridge"},
	}
	d.Containers[0].NetworkSettings = &types.SummaryNetworkSettings{
		Networks: map[string]*network.EndpointSettings{"deploy_octopus-net": {NetworkID: "n-net"}},
	}
	return d
}

func baseOptions() dockergc.Options {
	return dockergc.Options{
		Keep:            []string{"ghcr.io/misterdonnuts/octopus:dev-a", "octopus-agent:local"},
		ComposeProjects: []string{"deploy", "tools"},
		ContainerMinAge: time.Hour,
		Now:             now,
	}
}

func imageNames(items []dockergc.ImageItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name())
	}
	sort.Strings(out)
	return out
}

func containerNames(items []dockergc.ContainerItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}
	sort.Strings(out)
	return out
}

func TestScanRemovesOlderOctopusImagesOnly(t *testing.T) {
	d := host()
	plan, err := dockergc.Scan(context.Background(), d, baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"deploy-api:latest",
		"failedbuild",
		"ghcr.io/misterdonnuts/octopus-agent:0.1.0",
		"ghcr.io/misterdonnuts/octopus:0.1.0",
		"ghcr.io/misterdonnuts/octopus:dev-a-prev",
		"ghcr.io/misterdonnuts/octopus:dev-b",
		"intermediate",
		"octopus-daemon:latest",
	}
	if got := imageNames(plan.Images); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("images removed:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	kept := map[string]string{}
	for _, k := range plan.Kept {
		kept[k.Name()] = k.Reason
	}
	for name, reason := range map[string]string{
		"ghcr.io/misterdonnuts/octopus:dev-a": "in use by deploy-octopus-1",
		"octopus-agent:local":                 "in use by agent-fresh",
		"thulu-cloud:latest":                  "in use by thulu-cloud",
		"octopus-docker-agent:latest":         "nothing configured to compare it with",
	} {
		if !strings.HasPrefix(kept[name], reason) {
			t.Errorf("%s kept for %q, want %q", name, kept[name], reason)
		}
	}
	for _, it := range plan.Images {
		for _, r := range it.Refs {
			for _, foreign := range []string{"billing-auth", "mongo", "alpine", "acme/octopus"} {
				if strings.Contains(r, foreign) {
					t.Errorf("plan removes a foreign image %s", r)
				}
			}
		}
		if it.ID == "sha256:foreigndangling" {
			t.Error("plan removes a dangling image that is not Thulu's")
		}
	}
}

func TestScanTakesOnlyStoppedEphemeralContainers(t *testing.T) {
	plan, err := dockergc.Scan(context.Background(), host(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agent-old", "charming_tesla"}
	if got := containerNames(plan.Containers); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("containers = %v, want %v", got, want)
	}
}

func TestScanVolumesAndNetworks(t *testing.T) {
	plan, err := dockergc.Scan(context.Background(), host(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Volumes) != 1 || plan.Volumes[0].Name != "deploy_go-mod-cache" {
		t.Errorf("volumes = %+v, want only the unused cache volume (never data)", plan.Volumes)
	}
	var nets []string
	for _, n := range plan.Networks {
		nets = append(nets, n.Name)
	}
	if strings.Join(nets, ",") != "deploy_edge,octopus-agents" {
		t.Errorf("networks = %v", nets)
	}
}

func TestScanKeepsNetworksOfAStoppedStack(t *testing.T) {
	d := host()
	d.Containers[0].State = "exited"
	plan, err := dockergc.Scan(context.Background(), d, baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range plan.Networks {
		if n.Name == "deploy_octopus-net" {
			t.Error("the network of a stopped stack is planned for removal")
		}
	}
}

func TestRollbackImageKeptInsideItsWindow(t *testing.T) {
	opts := baseOptions()
	opts.Rollback = []string{"ghcr.io/misterdonnuts/octopus:dev-a-prev"}
	opts.RollbackUntil = now.Add(24 * time.Hour)
	plan, err := dockergc.Scan(context.Background(), host(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range plan.Images {
		if it.Name() == "ghcr.io/misterdonnuts/octopus:dev-a-prev" {
			t.Fatal("rollback image removed inside its window")
		}
	}

	opts.RollbackUntil = now.Add(-time.Hour)
	plan, err = dockergc.Scan(context.Background(), host(), opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range plan.Images {
		if it.Name() == "ghcr.io/misterdonnuts/octopus:dev-a-prev" {
			found = strings.HasPrefix(it.Reason, "rollback window ended")
		}
	}
	if !found {
		t.Error("rollback image kept after its window, or removed for the wrong reason")
	}
}

func TestUnclaimedDaemonImagesStayWithoutConfiguration(t *testing.T) {
	d := host()
	d.Containers = d.Containers[1:]
	plan, err := dockergc.Scan(context.Background(), d, dockergc.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range plan.Images {
		if it.Kind == dockergc.ImageDaemon {
			t.Errorf("unclaimed daemon image %s planned for removal", it.Name())
		}
	}
}

func TestPruneUnclaimedTakesUnusedComponents(t *testing.T) {
	opts := baseOptions()
	opts.PruneUnclaimed = true
	plan, err := dockergc.Scan(context.Background(), host(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(imageNames(plan.Images), "octopus-docker-agent:latest") {
		t.Error("octopus-docker-agent:latest kept although the keep set was declared complete")
	}
}

func TestImagesOnlyLeavesContainersVolumesNetworks(t *testing.T) {
	opts := baseOptions()
	opts.ImagesOnly = true
	plan, err := dockergc.Scan(context.Background(), host(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Containers)+len(plan.Volumes)+len(plan.Networks) != 0 {
		t.Errorf("images-only plan touches more: %+v", plan)
	}
	if contains(imageNames(plan.Images), "failedbuild") {
		t.Error("an image still used by a container is planned for removal")
	}
}

func TestUninstallTakesEverythingOctopusButDataUnlessPurged(t *testing.T) {
	opts := baseOptions()
	opts.Uninstall = true
	plan, err := dockergc.Scan(context.Background(), host(), opts)
	if err != nil {
		t.Fatal(err)
	}
	names := containerNames(plan.Containers)
	for _, want := range []string{"deploy-octopus-1", "thulu-cloud", "agent-fresh", "sandbox-up", "charming_tesla"} {
		if !contains(names, want) {
			t.Errorf("uninstall leaves container %s", want)
		}
	}
	for _, foreign := range []string{"billing-auth-app", "e2e-mongodb", "admiring_ellis"} {
		if contains(names, foreign) {
			t.Errorf("uninstall removes foreign container %s", foreign)
		}
	}
	images := imageNames(plan.Images)
	for _, want := range []string{"ghcr.io/misterdonnuts/octopus:dev-a", "octopus-agent:local", "octopus-docker-agent:latest"} {
		if !contains(images, want) {
			t.Errorf("uninstall keeps image %s", want)
		}
	}
	for _, v := range plan.Volumes {
		if strings.Contains(v.Name, "octopus-data") || strings.Contains(v.Name, "octopus-pg") {
			t.Errorf("uninstall without -purge-data removes %s", v.Name)
		}
	}

	opts.PurgeData = true
	plan, err = dockergc.Scan(context.Background(), host(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var vols []string
	for _, v := range plan.Volumes {
		vols = append(vols, v.Name)
	}
	if strings.Join(vols, ",") != "deploy_go-mod-cache,deploy_octopus-data,deploy_octopus-pg" {
		t.Errorf("purge volumes = %v", vols)
	}
}

func TestApplyRemovesWhatThePlanLists(t *testing.T) {
	d := host()
	plan, err := dockergc.Scan(context.Background(), d, baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	res := dockergc.Apply(context.Background(), d, plan)
	if err := res.Err(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Containers != 2 || res.Images != len(plan.Images) || res.Volumes != 1 || res.Networks != 2 {
		t.Errorf("result = %+v", res)
	}
	left := map[string]bool{}
	for _, img := range d.Images {
		left[img.ID] = true
	}
	for _, id := range []string{"sha256:dev-a", "sha256:agentlocal", "sha256:billingauth", "sha256:mongo", "sha256:alpine",
		"sha256:foreigndangling", "sha256:otheroctopus", "sha256:dockeragent", "sha256:mcpcloud"} {
		if !left[id] {
			t.Errorf("apply removed %s", id)
		}
	}
	for _, id := range []string{"sha256:dev-b", "sha256:prev", "sha256:failedbuild", "sha256:intermediate"} {
		if left[id] {
			t.Errorf("apply left %s", id)
		}
	}
}

func TestApplyNeverForcesAnImage(t *testing.T) {
	d := host()
	plan, err := dockergc.Scan(context.Background(), d, baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	d.Containers = append(d.Containers, dockergctest.Container("c-late", "late", "octopus-daemon:latest",
		"sha256:daemonlocal", "created", nil))
	res := dockergc.Apply(context.Background(), d, plan)
	if res.Err() == nil {
		t.Fatal("removing an image in use did not fail")
	}
	if res.Images != len(plan.Images)-1 {
		t.Errorf("images removed = %d, want all but one", res.Images)
	}
}

func TestSharedImageIsOnlyUntagged(t *testing.T) {
	d := &dockergctest.Docker{}
	shared := dockergctest.Image("sha256:shared", 100e6, "ghcr.io/misterdonnuts/octopus:0.0.9", "mycorp/custom:1")
	d.Images = append(d.Images, shared, dockergctest.Image("sha256:cur", 100e6, "ghcr.io/misterdonnuts/octopus:1.0.0"))
	plan, err := dockergc.Scan(context.Background(), d, dockergc.Options{
		Keep: []string{"ghcr.io/misterdonnuts/octopus:1.0.0"}, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Images) != 1 || !plan.Images[0].Shared || plan.ImageBytes() != 0 {
		t.Fatalf("plan = %+v", plan.Images)
	}
	if got := plan.Images[0].Refs; len(got) != 1 || got[0] != "ghcr.io/misterdonnuts/octopus:0.0.9" {
		t.Errorf("refs = %v, want only the Thulu tag", got)
	}
}

func TestMirrorRegistryIsOctopus(t *testing.T) {
	d := &dockergctest.Docker{}
	d.Images = append(d.Images,
		dockergctest.Image("sha256:a", 1, "registry.corp.local/octopus/octopus:1.0.0"),
		dockergctest.Image("sha256:b", 1, "registry.corp.local/octopus/octopus:1.1.0"),
		dockergctest.Image("sha256:c", 1, "registry.corp.local/octopus/octopus-mcp-github:1.0.0"),
		dockergctest.Image("sha256:d", 1, "registry.corp.local/octopus/octopus-mcp-github:1.1.0"),
	)
	getenv := func(k string) string {
		return map[string]string{
			"OCTOPUS_IMAGE":          "registry.corp.local/octopus/octopus:1.1.0",
			"OCTOPUS_IMAGE_REGISTRY": "registry.corp.local/octopus",
			"OCTOPUS_VERSION":        "1.1.0",
		}[k]
	}
	plan, err := dockergc.Scan(context.Background(), d, dockergc.Options{
		Keep: dockergc.KeepFromEnv(getenv), Registries: dockergc.RegistriesFromEnv(getenv), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "registry.corp.local/octopus/octopus-mcp-github:1.0.0,registry.corp.local/octopus/octopus:1.0.0"
	if got := strings.Join(imageNames(plan.Images), ","); got != want {
		t.Errorf("removed %s, want %s", got, want)
	}
}

func TestNewerVersionThanTheRunningOneIsKept(t *testing.T) {
	daemonLabels := func(v string) map[string]string {
		return map[string]string{"org.opencontainers.image.source": octopusSource, "org.opencontainers.image.version": v}
	}
	d := &dockergctest.Docker{Finished: map[string]string{}}
	add := func(id string, labels map[string]string, digests []string, tags ...string) {
		i := dockergctest.Image(id, 100e6, tags...)
		i.Labels, i.RepoDigests = labels, digests
		d.Images = append(d.Images, i)
	}
	add("sha256:v020", daemonLabels("0.2.0"), nil, "ghcr.io/misterdonnuts/octopus:0.2.0")
	add("sha256:v010", daemonLabels("0.1.0"), nil, "ghcr.io/misterdonnuts/octopus:0.1.0")
	add("sha256:v030rc", daemonLabels("0.3.0-rc.1"), nil, "ghcr.io/misterdonnuts/octopus:0.3.0-rc.1")
	add("sha256:mcp030", nil, nil, "ghcr.io/misterdonnuts/octopus-mcp-github:0.3.0")
	add("sha256:mcp020", nil, nil, "ghcr.io/misterdonnuts/octopus-mcp-github:0.2.0")
	add("sha256:mcp010", nil, nil, "ghcr.io/misterdonnuts/octopus-mcp-github:0.1.0")
	add("sha256:v030", daemonLabels("0.3.0"), []string{"ghcr.io/misterdonnuts/octopus@sha256:aaa"}, "<none>:<none>")
	d.Containers = []types.Container{
		dockergctest.Container("c-daemon", "deploy-octopus-1", "ghcr.io/misterdonnuts/octopus:0.2.0", "sha256:v020", "running", nil),
	}

	opts := dockergc.Options{
		Keep: []string{
			"ghcr.io/misterdonnuts/octopus:0.2.0",
			"ghcr.io/misterdonnuts/octopus-mcp-github:0.2.0",
		},
		Running: "0.2.0",
		Now:     now,
	}
	plan, err := dockergc.Scan(context.Background(), d, opts)
	if err != nil {
		t.Fatal(err)
	}
	removed := strings.Join(imageNames(plan.Images), ",")
	want := "ghcr.io/misterdonnuts/octopus-mcp-github:0.1.0,ghcr.io/misterdonnuts/octopus:0.1.0"
	if removed != want {
		t.Errorf("removed %s, want %s", removed, want)
	}
	kept := map[string]string{}
	for _, k := range plan.Kept {
		kept[k.ID] = k.Reason
	}
	for _, id := range []string{"sha256:v030", "sha256:v030rc", "sha256:mcp030"} {
		if !strings.HasPrefix(kept[id], "newer than the running 0.2.0") {
			t.Errorf("%s kept for %q, want the newer-version rule", id, kept[id])
		}
	}

	opts.Running = "dev"
	plan, err = dockergc.Scan(context.Background(), d, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(imageNames(plan.Images), "ghcr.io/misterdonnuts/octopus-mcp-github:0.3.0") {
		t.Errorf("with an unparseable Running the newer MCP image should be an older-version candidate; removed %v", imageNames(plan.Images))
	}
}

func TestImagesBuiltAfterTheKeptOneStay(t *testing.T) {
	daemonLabels := map[string]string{"org.opencontainers.image.source": octopusSource}
	d := &dockergctest.Docker{Finished: map[string]string{}}
	add := func(id string, created time.Time, labels map[string]string, tags ...string) {
		i := dockergctest.Image(id, 100e6, tags...)
		i.Labels, i.Created = labels, created.Unix()
		d.Images = append(d.Images, i)
	}
	weeksAgo, yesterday, today := now.Add(-21*24*time.Hour), now.Add(-24*time.Hour), now.Add(-time.Hour)
	add("sha256:devmain", weeksAgo, daemonLabels, "ghcr.io/misterdonnuts/octopus:devmain")
	add("sha256:devold", weeksAgo.Add(-time.Hour), daemonLabels, "ghcr.io/misterdonnuts/octopus:devold")
	add("sha256:e2e", today, daemonLabels, "octopus:runner-e2e")
	add("sha256:agentlocal", weeksAgo, nil, "octopus-agent:local")
	add("sha256:agente2e", yesterday, nil, "ghcr.io/misterdonnuts/octopus-agent:runner-e2e")
	add("sha256:agentold", weeksAgo.Add(-time.Hour), nil, "ghcr.io/misterdonnuts/octopus-agent:0.3.1")
	d.Containers = []types.Container{
		dockergctest.Container("c-daemon", "deploy-octopus-1", "ghcr.io/misterdonnuts/octopus:devmain", "sha256:devmain", "running", nil),
	}
	plan, err := dockergc.Scan(context.Background(), d, dockergc.Options{
		Keep: []string{"ghcr.io/misterdonnuts/octopus:devmain", "octopus-agent:local"},
		Now:  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	removed := strings.Join(imageNames(plan.Images), ",")
	if want := "ghcr.io/misterdonnuts/octopus-agent:0.3.1,ghcr.io/misterdonnuts/octopus:devold"; removed != want {
		t.Errorf("removed %s, want %s", removed, want)
	}
	kept := map[string]string{}
	for _, k := range plan.Kept {
		kept[k.ID] = k.Reason
	}
	for _, id := range []string{"sha256:e2e", "sha256:agente2e"} {
		if !strings.HasPrefix(kept[id], "built after the version this install uses") {
			t.Errorf("%s kept for %q, want the built-after rule", id, kept[id])
		}
	}
}

func TestKeptVersionsByDigest(t *testing.T) {
	env := map[string]string{
		"OCTOPUS_VERSION":          "0.3.0",
		"OCTOPUS_MCP_GITHUB_IMAGE": "ghcr.io/misterdonnuts/octopus-mcp-github@sha256:ccc",
		"OCTOPUS_KEPT_IMAGES":      "ghcr.io/misterdonnuts/octopus@sha256:aaa, ghcr.io/misterdonnuts/octopus-agent@sha256:bbb\n",
	}
	keep := dockergc.KeepFromEnv(func(k string) string { return env[k] })
	for _, want := range []string{
		"ghcr.io/misterdonnuts/octopus@sha256:aaa",
		"ghcr.io/misterdonnuts/octopus-agent@sha256:bbb",
		"ghcr.io/misterdonnuts/octopus-mcp-github@sha256:ccc",
	} {
		if !contains(keep, want) {
			t.Errorf("keep %v misses %s", keep, want)
		}
	}

	d := &dockergctest.Docker{Finished: map[string]string{}}
	old := dockergctest.Image("sha256:old", 100e6, "ghcr.io/misterdonnuts/octopus:0.1.0")
	old.RepoDigests = []string{"ghcr.io/misterdonnuts/octopus@sha256:aaa"}
	cur := dockergctest.Image("sha256:cur", 100e6, "ghcr.io/misterdonnuts/octopus:0.3.0")
	d.Images = append(d.Images, old, cur)
	plan, err := dockergc.Scan(context.Background(), d, dockergc.Options{Keep: keep, Running: "0.3.0", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Images) != 0 {
		t.Errorf("a kept version was planned for removal: %v", imageNames(plan.Images))
	}
}

func TestEngineImageFamilies(t *testing.T) {
	const reg = "ghcr.io/misterdonnuts/"
	env := map[string]string{
		"OCTOPUS_VERSION":                 "0.3.3",
		"OCTOPUS_AGENT_CLAUDE_CODE_IMAGE": reg + "octopus-agent-claude-code@sha256:c33",
		"OCTOPUS_AGENT_CODEX_IMAGE":       reg + "octopus-agent-codex@sha256:x33",
		"OCTOPUS_KEPT_IMAGES":             reg + "octopus-agent@sha256:a32",
	}
	keep := dockergc.KeepFromEnv(func(k string) string { return env[k] })
	for _, want := range []string{env["OCTOPUS_AGENT_CLAUDE_CODE_IMAGE"], env["OCTOPUS_AGENT_CODEX_IMAGE"]} {
		if !contains(keep, want) {
			t.Errorf("keep %v misses %s", keep, want)
		}
	}

	d := &dockergctest.Docker{Finished: map[string]string{}}
	add := func(id string, created time.Time, digest string, tag string) {
		i := dockergctest.Image(id, 100e6, tag)
		i.Created = created.Unix()
		if digest != "" {
			i.RepoDigests = []string{digest}
		}
		d.Images = append(d.Images, i)
	}
	add("sha256:claude33", now.Add(-time.Hour), env["OCTOPUS_AGENT_CLAUDE_CODE_IMAGE"], reg+"octopus-agent-claude-code:0.3.3")
	add("sha256:claudeold", now.Add(-48*time.Hour), "", reg+"octopus-agent-claude-code:0.3.3-rc1")
	add("sha256:combined32", now.Add(-72*time.Hour), reg+"octopus-agent@sha256:a32", reg+"octopus-agent:0.3.2")
	add("sha256:combined31", now.Add(-96*time.Hour), "", reg+"octopus-agent:0.3.1")
	plan, err := dockergc.Scan(context.Background(), d, dockergc.Options{Keep: keep, Running: "0.3.3", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	removed := strings.Join(imageNames(plan.Images), ",")
	if want := reg + "octopus-agent-claude-code:0.3.3-rc1," + reg + "octopus-agent:0.3.1"; removed != want {
		t.Errorf("removed %s, want %s", removed, want)
	}
}

func TestMCPImageEnv(t *testing.T) {
	if got := dockergc.MCPImageEnv("thulu-github"); got != "OCTOPUS_MCP_GITHUB_IMAGE" {
		t.Errorf("MCPImageEnv = %q", got)
	}
}

func TestKeepFromEnv(t *testing.T) {
	env := map[string]string{
		"OCTOPUS_AGENT_IMAGE": "octopus-agent:local",
		"OCTOPUS_VERSION":     "1.2.0",
	}
	keep := dockergc.KeepFromEnv(func(k string) string { return env[k] })
	for _, want := range []string{
		"octopus:latest", "ghcr.io/misterdonnuts/octopus:1.2.0", "octopus-agent:local", "octopus-runtime:latest",
		"ghcr.io/misterdonnuts/octopus-mcp-browser:1.2.0",
	} {
		if !contains(keep, want) {
			t.Errorf("keep %v misses %s", keep, want)
		}
	}
}

func TestRollbackFromEnv(t *testing.T) {
	env := map[string]string{
		dockergc.EnvRollbackImage: "ghcr.io/misterdonnuts/octopus:1.0.0",
		dockergc.EnvRollbackUntil: "2026-10-05T12:00:00Z",
	}
	imgs, until := dockergc.RollbackFromEnv(func(k string) string { return env[k] })
	if len(imgs) != 1 || !until.Equal(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v until %v", imgs, until)
	}
	env[dockergc.EnvRollbackUntil] = "soon"
	if imgs, _ := dockergc.RollbackFromEnv(func(k string) string { return env[k] }); imgs != nil {
		t.Error("an unparseable deadline still keeps the image")
	}
	for v, want := range map[string]int{"": 7, "3": 3, "0": 0, "x": 7, "-1": 7} {
		if got := dockergc.RollbackDays(func(string) string { return v }); got != want {
			t.Errorf("RollbackDays(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestNativeMCPServersMatchTheWizard(t *testing.T) {
	var wizard []string
	for _, s := range initwizard.NativeMCPServers {
		wizard = append(wizard, s.Name)
	}
	sort.Strings(wizard)
	ours := append([]string(nil), dockergc.NativeMCPServers...)
	sort.Strings(ours)
	if strings.Join(wizard, ",") != strings.Join(ours, ",") {
		t.Errorf("dockergc.NativeMCPServers = %v, initwizard has %v", ours, wizard)
	}
}

func TestDiscoverProjects(t *testing.T) {
	d := host()
	got, err := dockergc.DiscoverProjects(context.Background(), d, "deploy", "/opt/app/deploy", "/opt/app/tools")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "deploy,tools" {
		t.Errorf("projects = %v", got)
	}

	d.Containers = nil
	got, err = dockergc.DiscoverProjects(context.Background(), d, "deploy", "/opt/app/deploy")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "deploy" {
		t.Errorf("projects of a stopped stack = %v", got)
	}
	got, err = dockergc.DiscoverProjects(context.Background(), d, "shop", "/opt/app/deploy")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("accepted %v on a name alone", got)
	}
}

func TestSelfComposeProject(t *testing.T) {
	d := &dockergctest.Docker{Self: map[string]types.ContainerJSON{}}
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname")
	}
	d.Self[host] = types.ContainerJSON{Config: &container.Config{Labels: map[string]string{
		"com.docker.compose.project": "deploy",
	}}}
	if got := dockergc.SelfComposeProject(context.Background(), d); got != "deploy" {
		t.Errorf("SelfComposeProject = %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 189e6: "189.0 MB", 1_900_000_000: "1.9 GB"} {
		if got := dockergc.HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
