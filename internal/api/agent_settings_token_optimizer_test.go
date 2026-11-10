package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestAgentSettingsTokenOptimizerRoundTrip(t *testing.T) {
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{agentsDir: t.TempDir(), boards: stagepkg.StaticBoards()}
	srv.SetAgentStore(store)
	t.Setenv(OperatorWorkspaceEnv, "")
	const ws = "ws-tokopt"

	created := httptest.NewRecorder()
	srv.handleCreateAgent(created, tenantRequestBody(http.MethodPost, "/types", ws, `{"name":"developer"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create agent: %d %s", created.Code, created.Body.String())
	}
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := tenantRequestBody(http.MethodPut, "/types/developer/settings", ws, body)
		r.SetPathValue("name", "developer")
		w := httptest.NewRecorder()
		srv.handlePutAgentSettings(w, r)
		return w
	}
	get := func() AgentSettings {
		t.Helper()
		r := tenantRequestBody(http.MethodGet, "/types/developer/settings", ws, "")
		r.SetPathValue("name", "developer")
		w := httptest.NewRecorder()
		srv.handleGetAgentSettings(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET = %d: %s", w.Code, w.Body)
		}
		var out AgentSettings
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := get().TokenOptimizer; got == nil || *got != (AgentTokenOptimizer{Level: "off"}) {
		t.Fatalf("an agent without a policy = %+v, want level off and every key present", got)
	}
	hash := func() string {
		t.Helper()
		a, err := store.GetAgent(t.Context(), ws, "developer")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(a.AgentType)
		return string(b)
	}
	before := hash()

	if w := put(`{"name":"developer","token_optimizer":{"level":"off","engine":"claude-code","model":"haiku"}}`); w.Code != http.StatusOK {
		t.Fatalf("PUT off = %d: %s", w.Code, w.Body)
	}
	if after := hash(); after != before || strings.Contains(after, "token_optimizer") {
		t.Fatalf("off changed the definition:\n%s\n%s", before, after)
	}

	w := put(`{"name":"developer","valid_stages":["in_progress","review"],
		"token_optimizer":{"level":"Enforce","engine":" claude-code ","model":"claude-haiku-4-5"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body)
	}
	want := AgentTokenOptimizer{Level: "enforce", Engine: "claude-code", Model: "claude-haiku-4-5"}
	if got := get().TokenOptimizer; *got != want {
		t.Fatalf("stored = %+v", got)
	}

	if w := put(`{"name":"developer","valid_stages":["in_progress"]}`); w.Code != http.StatusOK {
		t.Fatalf("PUT without the field = %d: %s", w.Code, w.Body)
	}
	if got := get().TokenOptimizer; *got != want {
		t.Fatalf("a save without the field dropped the policy: %+v", got)
	}

	for name, body := range map[string]string{
		"level":   `{"name":"developer","token_optimizer":{"level":"loud"}}`,
		"firm":    `{"name":"developer","token_optimizer":{"level":"firm"}}`,
		"engine":  `{"name":"developer","token_optimizer":{"level":"suggest","model":"haiku"}}`,
		"unknown": `{"name":"developer","token_optimizer":{"level":"suggest","engine":"aider","model":"haiku"}}`,
		"model":   `{"name":"developer","token_optimizer":{"level":"suggest","engine":"claude-code"}}`,
	} {
		if w := put(body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d, want 400", name, w.Code)
		}
	}
	if got := get().TokenOptimizer; *got != want {
		t.Fatalf("a refused save changed the policy: %+v", got)
	}
}

func TestTokenOptimizerLabelsOnSave(t *testing.T) {
	preserved := map[string]string{
		types.LabelTokenOptimizer:         "suggest",
		types.LabelTokenOptimizerEngine:   "claude-code",
		types.LabelTokenOptimizerModel:    "haiku",
		types.LabelTokenOptimizer + ".qa": "off",
		"thulu.project":                   "p",
	}
	kept := buildAgentLabels("dev", AgentSettings{}, maps.Clone(preserved))
	if kept[types.LabelTokenOptimizer] != "suggest" || kept[types.LabelTokenOptimizerModel] != "haiku" {
		t.Fatalf("absent field dropped the labels: %v", kept)
	}
	replaced := buildAgentLabels("dev", AgentSettings{TokenOptimizer: &AgentTokenOptimizer{
		Level: "enforce", Engine: "codex", Model: "gpt-5-mini",
	}}, maps.Clone(preserved))
	if replaced[types.LabelTokenOptimizer] != "enforce" || replaced[types.LabelTokenOptimizerEngine] != "codex" ||
		replaced[types.LabelTokenOptimizerModel] != "gpt-5-mini" || replaced["thulu.project"] != "p" {
		t.Fatalf("replaced = %v", replaced)
	}
	if _, ok := replaced[types.LabelTokenOptimizer+".qa"]; ok {
		t.Fatalf("an old stage override survived the replace: %v", replaced)
	}
	off := buildAgentLabels("dev", AgentSettings{TokenOptimizer: &AgentTokenOptimizer{Level: "off"}}, maps.Clone(preserved))
	for k := range off {
		if types.IsTokenOptimizerLabel(k) {
			t.Fatalf("off kept %s: %v", k, off)
		}
	}
}
