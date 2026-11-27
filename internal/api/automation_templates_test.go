package api

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

// shippedDeployTemplates returns the specs of the factory templates that listen to deploy events.
func shippedDeployTemplates(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("../../web/src/lib/automation-templates.ts")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile("(?s)spec: `(.*?)`,\n").FindAllStringSubmatch(string(src), -1) {
		spec := strings.ReplaceAll(m[1], "\\`", "`")
		if strings.Contains(spec, "event: "+automation.EventDeployment) ||
			strings.Contains(spec, "event: "+automation.EventWorkflowRun) {
			out = append(out, spec)
		}
	}
	return out
}

// The watch-deploy templates name the Thulu board, its release type and its Release column: with
// the flow installed, Save accepts them as they ship.
func TestIntegration_DeployTemplatesSaveOnTheThuluFlow(t *testing.T) {
	t.Parallel()
	srv, h, token := newFlowServer(t)
	store, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(store, automation.Deps{})
	srv.SetAutomations(store, engine, automation.NewScheduler(store, engine))

	specs := shippedDeployTemplates(t)
	if len(specs) != 2 {
		t.Fatalf("found %d deploy templates, want 2", len(specs))
	}
	validate := func(spec string) (bool, string) {
		w := postJSONAuth(t, h, "/automations/validate", token, map[string]string{"spec": spec})
		var out struct {
			Valid bool   `json:"valid"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("validate = %d: %s", w.Code, w.Body.String())
		}
		return out.Valid, out.Error
	}
	if ok, _ := validate(specs[0]); ok {
		t.Error("a deploy template validated on a workspace without the Thulu board")
	}
	applyThulu(t, h, token)
	for _, spec := range specs {
		if ok, msg := validate(spec); !ok {
			t.Errorf("template %q refused: %s", firstSpecLine(spec), msg)
		}
	}
}

func firstSpecLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
