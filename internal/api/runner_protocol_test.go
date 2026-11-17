package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

// Case 25: runners of protocol 1 and 2 are served and recorded; anything else is told to
// update.
func TestRunnerProtocolRange(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{runnerStore: f.store, runnerRoute: &routedLauncher{runner: f.launcher}}
	var seen *runner.Runner
	h := srv.runnerAuth(func(w http.ResponseWriter, r *http.Request) { seen = callingRunner(r) })
	call := func(protocol string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/runner/v1/claim", nil)
		r.Header.Set(runner.ProtocolHeader, protocol)
		r.Header.Set("Authorization", "Bearer "+f.reg.Credential)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	for _, p := range []string{"1", "2"} {
		seen = nil
		if w := call(p); w.Code != http.StatusOK || seen == nil || seen.Protocol != runner.AcceptedProtocol(p) {
			t.Errorf("protocol %s = %d %s, runner %+v", p, w.Code, w.Body.String(), seen)
		}
	}
	for _, p := range []string{"3", "0", ""} {
		if w := call(p); w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "Update the runner") {
			t.Errorf("protocol %q = %d %s, want told to update", p, w.Code, w.Body.String())
		}
	}
}
