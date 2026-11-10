package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestLaunchCtxCarriesThePerson(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/tasks/t1/move", nil)
	signedIn := r.WithContext(auth.WithClaims(context.Background(), &auth.Claims{UserID: "u_ada", Role: auth.RoleDeveloper}))
	if got := task.TriggeredBy(launchCtx(signedIn.Context())); got != "u_ada" {
		t.Errorf("signed-in user = %q, want u_ada", got)
	}
	if got := task.TriggeredBy(launchCtx(asDevOperator(httptest.NewRequest(http.MethodPost, "/x", nil)).Context())); got != "dev-mode" {
		t.Errorf("dev session = %q, want dev-mode", got)
	}
	if got := task.TriggeredBy(launchCtx(r.Context())); got != "" {
		t.Errorf("no credential = %q, want nobody", got)
	}
	ctx, cancel := context.WithCancel(signedIn.Context())
	launched := launchCtx(ctx)
	cancel()
	if launched.Err() != nil {
		t.Error("the launch context ended with the request")
	}
}

func TestEngineLoginsUnavailableWithoutDocker(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	if srv.personalLogins != nil {
		t.Skip("this server has Docker")
	}
	h = asDevBrowser(srv, h)
	if w := getJSON(t, h, "/me/engine-logins", ""); w.Code != http.StatusNotFound {
		t.Errorf("GET /me/engine-logins = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, h, "/me/engine-logins/claude-code", nil); w.Code != http.StatusNotFound {
		t.Errorf("POST /me/engine-logins/claude-code = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestPersonalLoginMount(t *testing.T) {
	env := map[string]string{"ENGINE": "claude-code", task.EnvPersonalLoginVolume: "octopus-login-claude-code-u1"}
	vols, err := personalLoginMount(env)
	if err != nil || len(vols) != 1 || vols[0].Source != "octopus-login-claude-code-u1" || vols[0].Target != "/home/node/.claude-login" {
		t.Fatalf("mount = %+v, %v", vols, err)
	}
	if _, ok := env[task.EnvPersonalLoginVolume]; ok {
		t.Error("the volume's name reached the run's env")
	}
	if vols, err := personalLoginMount(map[string]string{"ENGINE": "claude-code"}); err != nil || vols != nil {
		t.Errorf("a run on the workspace credential got %+v, %v", vols, err)
	}
	if _, err := personalLoginMount(map[string]string{"ENGINE": "nope", task.EnvPersonalLoginVolume: "v"}); err == nil {
		t.Error("an engine with no personal login took a volume")
	}
}
