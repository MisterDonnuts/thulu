package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

var infraPaths = []string{"/infra/overview", "/infra/history?range=15m", "/infra/containers"}

func TestInfra_TenantOwnersAreRefused(t *testing.T) {
	rig := newOperatorRig(t)
	rig.srv.SetInfra(infra.New(infra.Config{ProcRoot: filepath.Join(t.TempDir(), "none")}))
	for _, path := range infraPaths {
		for name, cred := range map[string][2]string{
			"tenant owner session": {rig.tenantToken, ""},
			"tenant owner key":     {"", rig.tenantAdminKey},
		} {
			w := callAs(t, rig.h, http.MethodGet, path, cred[0], cred[1], nil)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), OperatorWorkspaceEnv) {
				t.Errorf("GET %s as %s = %d, want 403 naming %s: %s", path, name, w.Code, OperatorWorkspaceEnv, w.Body.String())
			}
		}
	}
}

func TestInfra_OperatorReads(t *testing.T) {
	rig := newOperatorRig(t)

	if w := callAs(t, rig.h, http.MethodGet, "/infra/overview", rig.opToken, "", nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired overview as the operator = %d, want 503: %s", w.Code, w.Body.String())
	}
	if w := callAs(t, rig.h, http.MethodGet, "/infra/overview", rig.tenantToken, "", nil); w.Code != http.StatusForbidden {
		t.Errorf("unwired overview as a tenant = %d, want 403", w.Code)
	}

	c := infra.New(infra.Config{ProcRoot: filepath.Join(t.TempDir(), "none"), Interval: time.Hour})
	rig.srv.SetInfra(c)
	ctx, cancel := context.WithCancel(t.Context())
	go c.Run(ctx)
	waitFor(t, 5*time.Second, func() bool { return len(c.History(time.Hour)) > 0 })
	cancel()

	for _, cred := range [][2]string{{rig.opToken, ""}, {"", rig.opKey}} {
		w := callAs(t, rig.h, http.MethodGet, "/infra/overview", cred[0], cred[1], nil)
		if w.Code != http.StatusOK {
			t.Fatalf("overview as the operator = %d: %s", w.Code, w.Body.String())
		}
		var snap struct {
			Runtime struct{ Mode string } `json:"runtime"`
			Host    struct{ Threads int } `json:"host"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil || snap.Runtime.Mode != "none" || snap.Host.Threads == 0 {
			t.Errorf("overview body = %s (%v)", w.Body.String(), err)
		}
	}

	w := callAs(t, rig.h, http.MethodGet, "/infra/history?range=2h", rig.opToken, "", nil)
	var hist struct {
		Range    string           `json:"range"`
		Interval int              `json:"interval_seconds"`
		Points   []map[string]any `json:"points"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &hist); err != nil || w.Code != http.StatusOK || hist.Range != "2h" ||
		hist.Interval != 3600 || len(hist.Points) != 1 {
		t.Errorf("history = %d %s", w.Code, w.Body.String())
	}
	if w := callAs(t, rig.h, http.MethodGet, "/infra/history?range=7d", rig.opToken, "", nil); w.Code != http.StatusBadRequest {
		t.Errorf("history range=7d = %d, want 400", w.Code)
	}
	if w := callAs(t, rig.h, http.MethodGet, "/infra/containers", rig.opToken, "", nil); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"mode":"none"`) {
		t.Errorf("containers = %d %s", w.Code, w.Body.String())
	}
}

// The runtime card names the agent image the install built, or why it did not, in words.
func TestInfra_AgentImageBuildsAreLocalized(t *testing.T) {
	rig := newOperatorRig(t)
	failed := apperr.Unprocessable("kuberuntime.image_build.job_failed", "job", "octopus-builder/agent-image-1", "detail", "npm 503")
	c := infra.New(infra.Config{
		ProcRoot: filepath.Join(t.TempDir(), "none"), Interval: time.Hour,
		Kube: &infra.KubeSource{Client: fake.NewClientset(), AgentImages: func() []infra.AgentImageBuild {
			return []infra.AgentImageBuild{{Base: "ghcr.io/x/octopus-agent-claude-code:1", State: "failed", Err: failed}}
		}},
	})
	rig.srv.SetInfra(c)
	ctx, cancel := context.WithCancel(t.Context())
	go c.Run(ctx)
	waitFor(t, 5*time.Second, func() bool { return len(c.History(time.Hour)) > 0 })
	cancel()

	w := callAs(t, rig.h, http.MethodGet, "/infra/overview", rig.opToken, "", nil)
	var snap struct {
		Runtime struct {
			Kube struct {
				AgentImages []infra.AgentImageBuild `json:"agent_images"`
			} `json:"kube"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil || len(snap.Runtime.Kube.AgentImages) != 1 {
		t.Fatalf("overview = %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if msg := snap.Runtime.Kube.AgentImages[0].Message; !strings.Contains(msg, "npm 503") ||
		!strings.Contains(msg, "octopus-builder/agent-image-1") {
		t.Errorf("message = %q", msg)
	}
	if again := c.Overview().Runtime.Kube.AgentImages[0].Message; again != "" {
		t.Errorf("the handler wrote into the shared snapshot: %q", again)
	}
}

func TestInfraCache(t *testing.T) {
	rig := newOperatorRig(t)
	for name, cred := range map[string][2]string{
		"tenant owner session": {rig.tenantToken, ""},
		"tenant owner key":     {"", rig.tenantAdminKey},
	} {
		w := callAs(t, rig.h, http.MethodGet, "/infra/cache", cred[0], cred[1], nil)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), OperatorWorkspaceEnv) {
			t.Errorf("GET /infra/cache as %s = %d, want 403 naming %s: %s", name, w.Code, OperatorWorkspaceEnv, w.Body.String())
		}
	}

	if _, err := rig.srv.sandbox.store.RecordSetupRun(t.Context(), sandbox.SetupRun{
		RunKey: "task:x", WorkspaceID: rig.tenantWS, ProjectID: "p-gone", Kind: sandbox.SetupRunTask,
		Status: sandbox.SetupStateSkipped,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rig.srv.sandbox.store.RecordCacheUsage(t.Context(), sandbox.CacheUsage{
		WorkspaceID: rig.tenantWS, Source: sandbox.CacheSourceTask, TotalBytes: 1 << 20, MeasuredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, cred := range [][2]string{{rig.opToken, ""}, {"", rig.opKey}} {
		w := callAs(t, rig.h, http.MethodGet, "/infra/cache?range=30d", cred[0], cred[1], nil)
		var rep struct {
			Range  string `json:"range"`
			Totals struct {
				Runs    int      `json:"runs"`
				HitRate *float64 `json:"hit_rate"`
			} `json:"totals"`
			Daily      []map[string]any `json:"daily"`
			Workspaces []struct {
				WorkspaceID   string `json:"workspace_id"`
				WorkspaceName string `json:"workspace_name"`
				TotalBytes    int64  `json:"total_bytes"`
			} `json:"workspaces"`
			Projects []struct {
				ProjectID     string `json:"project_id"`
				WorkspaceName string `json:"workspace_name"`
			} `json:"projects"`
			Thresholds map[string]any `json:"thresholds"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || w.Code != http.StatusOK {
			t.Fatalf("cache = %d %s (%v)", w.Code, w.Body.String(), err)
		}
		if rep.Range != "30d" || len(rep.Daily) != 30 || rep.Totals.Runs != 1 || rep.Totals.HitRate == nil ||
			len(rep.Workspaces) != 1 || rep.Workspaces[0].WorkspaceName == "" || rep.Workspaces[0].TotalBytes != 1<<20 ||
			len(rep.Projects) != 1 || rep.Projects[0].WorkspaceName == "" || len(rep.Thresholds) == 0 {
			t.Errorf("cache report = %s", w.Body.String())
		}
	}
	if w := callAs(t, rig.h, http.MethodGet, "/infra/cache?range=2h", rig.opToken, "", nil); w.Code != http.StatusBadRequest {
		t.Errorf("cache range=2h = %d, want 400", w.Code)
	}
	if w := callAs(t, rig.h, http.MethodGet, "/infra/cache", rig.opToken, "", nil); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"range":"7d"`) {
		t.Errorf("cache default range = %d %s", w.Code, w.Body.String())
	}
}

func TestInfraStopContainer_GateAndUnknownID(t *testing.T) {
	rig := newOperatorRig(t)
	rig.srv.SetInfra(infra.New(infra.Config{ProcRoot: filepath.Join(t.TempDir(), "none")}))
	if w := callAs(t, rig.h, http.MethodPost, "/infra/containers/abc/stop", rig.tenantToken, "", nil); w.Code != http.StatusForbidden {
		t.Errorf("tenant stop = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := callAs(t, rig.h, http.MethodPost, "/infra/containers/abc/stop", rig.opToken, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("operator stop of an unknown id = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestSandboxOwnerOf_MatchesByContainerNotLabel(t *testing.T) {
	live := []*sandbox.Sandbox{
		{TaskID: "task-a", ContainerID: "0123abcd"},
		{TaskID: "prewarm:p1", ContainerID: "sandbox-prewarm-p1"},
		{TaskID: "evidence:b1", ContainerID: ""},
	}
	cases := []struct {
		name string
		c    infra.Container
		want string
	}{
		{"kube pod with a sanitized label", infra.Container{ID: "uid-1", Name: "sandbox-prewarm-p1", TaskID: "prewarm-p1"}, "prewarm:p1"},
		{"docker container", infra.Container{ID: "0123abcd", Name: "octopus-sandbox-task-a", TaskID: "task-a"}, "task-a"},
		{"no live row", infra.Container{ID: "uid-2", Name: "sandbox-evidence-b1", TaskID: "evidence-b1"}, ""},
	}
	for _, tc := range cases {
		if got := sandboxOwnerOf(&tc.c, live); got != tc.want {
			t.Errorf("%s: owner = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestInfraRunID(t *testing.T) {
	full := infra.Container{ID: "0123456789abcdef0123456789abcdef", Name: "octopus-agent-x"}
	if got := infraRunID(&full, false); got != "0123456789ab" {
		t.Errorf("docker run id = %q, want the 12-char id", got)
	}
	if got := infraRunID(&full, true); got != "octopus-agent-x" {
		t.Errorf("kube run id = %q, want the pod name", got)
	}
	short := infra.Container{ID: "abc"}
	if got := infraRunID(&short, false); got != "abc" {
		t.Errorf("short id = %q", got)
	}
}
