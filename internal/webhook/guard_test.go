package webhook

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDispatcher_RefusesInternalEndpoint(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "")
	d := NewDispatcher(t.Context(), newInternalStore(t))

	for _, u := range []string{ts.URL, "http://169.254.169.254/latest/meta-data/"} {
		code, errText := d.deliverOnce(t.Context(), &Endpoint{URL: u}, []byte(`{}`), "task.created", "d1")
		if code != 0 || !strings.Contains(errText, "egress policy blocks") {
			t.Errorf("%s: code=%d err=%q, want a blocked delivery", u, code, errText)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the internal endpoint received %d deliveries", hits.Load())
	}

	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "127.0.0.0/8")
	if code, errText := d.deliverOnce(t.Context(), &Endpoint{URL: ts.URL}, []byte(`{}`), "task.created", "d2"); code != http.StatusOK {
		t.Errorf("allowlisted endpoint: code=%d err=%q", code, errText)
	}
}
