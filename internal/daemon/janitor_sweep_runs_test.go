package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
)

// A run that ended while the daemon was down never ran its exit callback, the one that removes
// its browser. The minute sweep removes a browser whose agent is gone and keeps one whose agent
// still runs.
func TestSweepRunsRemovesTheBrowserOfARunThatEnded(t *testing.T) {
	var mu sync.Mutex
	var removed []string
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/json"):
			if !strings.Contains(r.URL.Query().Get("filters"), browserrun.LabelBrowser) {
				_ = json.NewEncoder(w).Encode([]dtypes.Container{})
				return
			}
			_ = json.NewEncoder(w).Encode([]dtypes.Container{
				{ID: "browser-of-gone", Labels: map[string]string{browserrun.LabelBrowser: "true", browserrun.LabelOwner: "agent-gone"}},
				{ID: "browser-of-live", Labels: map[string]string{browserrun.LabelBrowser: "true", browserrun.LabelOwner: "agent-live"}},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/agent-live/json"):
			_ = json.NewEncoder(w).Encode(dtypes.ContainerJSON{ContainerJSONBase: &dtypes.ContainerJSONBase{
				ID: "agent-live", State: &dtypes.ContainerState{Running: true}}})
		case r.Method == http.MethodDelete:
			mu.Lock()
			removed = append(removed, path[strings.LastIndex(path, "/")+1:])
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(engine.Close)
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+engine.Listener.Addr().String()), client.WithVersion("1.45"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	(&Janitor{docker: cli}).sweepRuns(t.Context())

	mu.Lock()
	defer mu.Unlock()
	if len(removed) != 1 || removed[0] != "browser-of-gone" {
		t.Fatalf("removed %v, want only the browser whose agent is gone", removed)
	}
}
