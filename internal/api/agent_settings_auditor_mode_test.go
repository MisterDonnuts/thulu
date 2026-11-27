package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type auditorModeRig struct {
	t     *testing.T
	srv   *Server
	store *agentstore.Store
	ws    string
}

func newAuditorModeRig(t *testing.T) *auditorModeRig {
	t.Helper()
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{agentsDir: t.TempDir(), boards: stagepkg.StaticBoards()}
	srv.SetAgentStore(store)
	t.Setenv(OperatorWorkspaceEnv, "")
	r := &auditorModeRig{t: t, srv: srv, store: store, ws: "ws-auditor-mode"}
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, tenantRequestBody(http.MethodPost, "/types", r.ws, `{"name":"dev"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create agent: %d %s", w.Code, w.Body.String())
	}
	return r
}

func (r *auditorModeRig) put(body string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := tenantRequestBody(http.MethodPut, "/types/dev/settings", r.ws, body)
	req.SetPathValue("name", "dev")
	w := httptest.NewRecorder()
	r.srv.handlePutAgentSettings(w, req)
	return w
}

func (r *auditorModeRig) get() AgentSettings {
	r.t.Helper()
	req := tenantRequestBody(http.MethodGet, "/types/dev/settings", r.ws, "")
	req.SetPathValue("name", "dev")
	w := httptest.NewRecorder()
	r.srv.handleGetAgentSettings(w, req)
	if w.Code != http.StatusOK {
		r.t.Fatalf("GET = %d: %s", w.Code, w.Body)
	}
	var out AgentSettings
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *auditorModeRig) stored() *types.AuditorSpec {
	r.t.Helper()
	a, err := r.store.GetAgent(context.Background(), r.ws, "dev")
	if err != nil {
		r.t.Fatal(err)
	}
	return a.Auditor
}

// A new agent reads "signals", and an auditor turned on without naming a mode is stored in it.
func TestAuditorModeDefaultsToSignalsForANewAuditor(t *testing.T) {
	r := newAuditorModeRig(t)
	if got := r.get().AuditorMode; got != types.AuditorModeSignals {
		t.Fatalf("new agent auditor_mode = %q, want signals", got)
	}
	if w := r.put(`{"valid_stages":["in_progress"],"auditor_stages":["in_progress"]}`); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body)
	}
	if spec := r.stored(); spec == nil || spec.Mode != types.AuditorModeSignals {
		t.Fatalf("stored auditor = %+v, want mode signals", spec)
	}
	if got := r.get().AuditorMode; got != types.AuditorModeSignals {
		t.Errorf("auditor_mode = %q, want signals", got)
	}
}

// An agent stored before modes existed keeps "always", and a save that does not name the mode
// leaves its definition as it was.
func TestAuditorModeKeepsAlwaysOnAnExistingAuditor(t *testing.T) {
	r := newAuditorModeRig(t)
	ctx := context.Background()
	a, err := r.store.GetAgent(ctx, r.ws, "dev")
	if err != nil {
		t.Fatal(err)
	}
	a.ValidStages = []types.Stage{types.StageInProgress}
	a.Auditor = &types.AuditorSpec{Stages: []string{"in_progress"}}
	if _, err := r.store.PutAgent(ctx, r.ws, a, "test"); err != nil {
		t.Fatal(err)
	}
	if got := r.get().AuditorMode; got != types.AuditorModeAlways {
		t.Fatalf("legacy auditor_mode = %q, want always", got)
	}
	if w := r.put(`{"description":"edited"}`); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body)
	}
	if spec := r.stored(); spec == nil || spec.Mode != "" {
		t.Errorf("stored auditor = %+v, want the empty (always) mode kept", spec)
	}
	// The full object the web UI sends back still says always.
	full := r.get()
	full.Description = "edited again"
	body, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if w := r.put(string(body)); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body)
	}
	if spec := r.stored(); spec == nil || spec.Mode != "" {
		t.Errorf("stored auditor = %+v after a full save, want always kept as empty", spec)
	}
}

func TestAuditorModeAndBudgetRoundTrip(t *testing.T) {
	r := newAuditorModeRig(t)
	if w := r.put(`{"valid_stages":["in_progress"],"auditor_stages":["in_progress"],
		"auditor_mode":"signals","auditor_max_diff_lines":900,"auditor_max_diff_files":30}`); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body)
	}
	got := r.get()
	if got.AuditorMode != types.AuditorModeSignals || got.AuditorMaxDiffLines != 900 || got.AuditorMaxDiffFiles != 30 {
		t.Errorf("settings = mode %q budget %d/%d", got.AuditorMode, got.AuditorMaxDiffLines, got.AuditorMaxDiffFiles)
	}
	if w := r.put(`{"auditor_mode":"always"}`); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body)
	}
	if spec := r.stored(); spec == nil || spec.Mode != "" || spec.MaxDiffLines != 900 {
		t.Errorf("stored auditor = %+v, want always (empty) with the budget kept", spec)
	}
	if got := r.get().AuditorMode; got != types.AuditorModeAlways {
		t.Errorf("auditor_mode = %q, want always", got)
	}
}

func TestAuditorModeRefusesUnknownValues(t *testing.T) {
	r := newAuditorModeRig(t)
	for _, body := range []string{
		`{"auditor_mode":"sometimes"}`,
		`{"auditor_max_diff_lines":-1}`,
		`{"auditor_max_diff_files":2000000}`,
	} {
		if w := r.put(body); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", body, w.Code)
		}
	}
}

// Compose labels carry signals and the budget; always stays the unlabeled default.
func TestApplyAuditorSettingsLabelsWritesModeAndBudget(t *testing.T) {
	base := AgentSettings{ValidStages: []string{"in_progress"}, AuditorStages: []string{"in_progress"}}
	labels := map[string]string{}
	signals := base
	signals.AuditorMode, signals.AuditorMaxDiffLines, signals.AuditorMaxDiffFiles = types.AuditorModeSignals, 600, 0
	applyAuditorSettingsLabels(labels, signals)
	if labels[types.LabelAuditorMode] != types.AuditorModeSignals || labels[types.LabelAuditorMaxDiffLines] != "600" {
		t.Errorf("labels = %v", labels)
	}
	if _, ok := labels[types.LabelAuditorMaxDiffFiles]; ok {
		t.Errorf("a default files budget was written: %v", labels)
	}
	labels = map[string]string{}
	always := base
	always.AuditorMode = types.AuditorModeAlways
	applyAuditorSettingsLabels(labels, always)
	if _, ok := labels[types.LabelAuditorMode]; ok {
		t.Errorf("always wrote a mode label: %v", labels)
	}
}
