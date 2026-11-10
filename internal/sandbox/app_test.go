package sandbox

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

func TestEvidenceOwner(t *testing.T) {
	if got := EvidenceOwner("b1"); got != "evidence:b1" {
		t.Fatalf("EvidenceOwner = %q", got)
	}
	for owner, want := range map[string]string{"evidence:b1": "b1", "evidence:": "", "prewarm:p1": "", "task-1": ""} {
		id, ok := EvidenceBatchID(owner)
		if id != want || ok != (want != "") {
			t.Errorf("EvidenceBatchID(%q) = %q, %v", owner, id, ok)
		}
	}
}

func appRig(t *testing.T) *initRig {
	t.Helper()
	return newInitRigOn(t, initScript("true", project.InitManifest{}), func(l *localRuntime) Runtime {
		return &peerRuntime{&publishedRuntime{localRuntime: l, ports: map[int]int{3000: 32001, 8080: 32004}}}
	})
}

type phaseLog struct {
	mu     sync.Mutex
	phases []string
}

func (p *phaseLog) add(phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phases = append(p.phases, phase)
}

func (p *phaseLog) list() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.phases)
}

func appSpec(r *initRig, owner string, sc *project.InitScript, phases *phaseLog) AppSpec {
	return AppSpec{WorkspaceID: "ws-1", ProjectID: "p-app", Name: "App", Dir: "app", Script: sc,
		HostPath: r.work, Owner: owner, Stage: "evidence", Progress: phases.add}
}

func TestRunApp_ComesUpOnTheFirstExposedPort(t *testing.T) {
	r := appRig(t)
	owner := EvidenceOwner("b-up")
	sc := initScript(`touch app.ready; echo "serving $OCTOPUS_PROJECT"; sleep 60`, project.InitManifest{
		Ports: []int{9999, 8080, 3000},
		Ready: project.InitReady{Command: "test -f app.ready", TimeoutSeconds: 10},
	})
	phases := &phaseLog{}
	res, err := r.h.mgr.RunApp(t.Context(), appSpec(r, owner, sc, phases))
	if err != nil {
		t.Fatalf("RunApp: %v", err)
	}
	info, err := r.h.mgr.PreviewURL(t.Context(), owner, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if res.Port != 8080 || res.URL != info.URL || !strings.Contains(res.Log, "serving App") {
		t.Fatalf("result = %+v, want port 8080 at %s", res, info.URL)
	}
	if got := phases.list(); len(got) < 2 || got[0] != AppPhaseSource || got[len(got)-1] != AppPhaseStart {
		t.Fatalf("phases = %v", got)
	}
	if _, err := r.h.mgr.store.Get(t.Context(), owner); err != nil {
		t.Fatalf("RunApp tore the sandbox down: %v", err)
	}
	if err := r.h.mgr.Destroy(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.mgr.store.Get(t.Context(), owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Destroy the row is still there: %v", err)
	}
}

func TestRunApp_PassesParamDefaults(t *testing.T) {
	r := appRig(t)
	owner := EvidenceOwner("b-params")
	sc := initScript(`set -u; echo "web on $WEB_PORT"; touch app.ready; sleep 60`, project.InitManifest{
		Params: []project.InitParam{{Name: "WEB_PORT", Default: "3000"}, {Name: "API_URL"}},
		Ports:  []int{3000},
		Ready:  project.InitReady{Command: "test -f app.ready", TimeoutSeconds: 10},
	})
	res, err := r.h.mgr.RunApp(t.Context(), appSpec(r, owner, sc, &phaseLog{}))
	if err != nil {
		t.Fatalf("RunApp: %v", err)
	}
	if !strings.Contains(res.Log, "web on 3000") {
		t.Fatalf("log = %q, want the default port", res.Log)
	}
	if err := r.h.mgr.Destroy(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
}

func TestRunApp_RequiredParamWithoutDefault(t *testing.T) {
	r := appRig(t)
	owner := EvidenceOwner("b-required")
	sc := initScript("echo started", project.InitManifest{
		Params: []project.InitParam{{Name: "API_KEY", Required: true}},
		Ports:  []int{3000},
	})
	_, err := r.h.mgr.RunApp(t.Context(), appSpec(r, owner, sc, &phaseLog{}))
	var ae *AppError
	if !errors.As(err, &ae) || ae.Reason != AppParamsMissing || !strings.Contains(ae.Log, "API_KEY") {
		t.Fatalf("RunApp error = %v, want %s naming API_KEY", err, AppParamsMissing)
	}
	if _, err := r.h.mgr.store.Get(t.Context(), owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a sandbox was created: %v", err)
	}
}

func TestRunApp_ClassifiesFailures(t *testing.T) {
	cases := []struct {
		name, script string
		manifest     project.InitManifest
		reason, code string
		log          string
	}{
		{"start exits", "echo boom >&2; exit 7", project.InitManifest{Ports: []int{3000}}, AppStartFailed, "7", "boom"},
		{"setup fails", "echo started", project.InitManifest{Ports: []int{3000}, Setup: "echo npm ERR >&2; exit 9"},
			AppSetupFailed, "", "npm ERR"},
		{"never ready", "sleep 60", project.InitManifest{Ports: []int{3000},
			Ready: project.InitReady{Command: "false", TimeoutSeconds: 1}}, AppReadyTimeout, "", ""},
		{"no exposed port", "echo started", project.InitManifest{Ports: []int{9999}}, AppNoPreviewPort, "", "started"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := appRig(t)
			owner := EvidenceOwner("b-fail-" + strconv.Itoa(i))
			_, err := r.h.mgr.RunApp(t.Context(), appSpec(r, owner, initScript(tc.script, tc.manifest), &phaseLog{}))
			var ae *AppError
			if !errors.As(err, &ae) {
				t.Fatalf("RunApp error = %v, want an *AppError", err)
			}
			if ae.Reason != tc.reason || ae.Code != tc.code || !strings.Contains(ae.Log, tc.log) {
				t.Fatalf("AppError = %+v, want reason %s code %q log with %q", ae, tc.reason, tc.code, tc.log)
			}
			if _, err := r.h.mgr.store.Get(t.Context(), owner); err != nil {
				t.Fatalf("a failed app's sandbox is the caller's to destroy: %v", err)
			}
		})
	}
}

func TestRunApp_NoRoom(t *testing.T) {
	r := appRig(t)
	ctx := t.Context()
	if err := r.h.mgr.settings.SetSandboxMax(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.h.mgr.settings.PutRaw(ctx, settings.SandboxAcquireBlockMsKey, "50"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.mgr.Acquire(ctx, "task-busy", "in_progress", AcquireDockerOpts{}); err != nil {
		t.Fatal(err)
	}
	owner := EvidenceOwner("b-room")
	_, err := r.h.mgr.RunApp(ctx, appSpec(r, owner, initScript("echo started", project.InitManifest{Ports: []int{3000}}), &phaseLog{}))
	var ae *AppError
	if !errors.As(err, &ae) || ae.Reason != AppNoRoom {
		t.Fatalf("RunApp error = %v, want no_room", err)
	}
	if r.h.mgr.queue.PendingGrant(owner) {
		t.Fatal("the queue still holds a grant for the app")
	}
	if n := len(r.rt.execs); n != 0 {
		t.Fatalf("%d commands ran without a sandbox", n)
	}
}

func TestRunApp_RefusesASpecWithoutOneSource(t *testing.T) {
	r := appRig(t)
	spec := appSpec(r, EvidenceOwner("b-src"), initScript("true", project.InitManifest{}), &phaseLog{})
	spec.HostPath = ""
	if _, err := r.h.mgr.RunApp(t.Context(), spec); err == nil {
		t.Fatal("a spec with no source was accepted")
	}
	spec.Script = nil
	if _, err := r.h.mgr.RunApp(t.Context(), spec); err == nil {
		t.Fatal("a spec with no init script was accepted")
	}
}
