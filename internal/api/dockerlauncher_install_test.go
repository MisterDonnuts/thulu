package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
)

func TestScanExitedTaskAgentsLeavesAnotherInstallsRunsOut(t *testing.T) {
	dockerx.SetInstallID("mine")
	t.Cleanup(func() { dockerx.SetInstallID("") })

	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/json") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]dtypes.Container{
			{ID: "aaaaaaaaaaaaaaaa", Labels: map[string]string{
				agent.LabelManaged: "true", agent.LabelInstance: "coder-mine-1", dockerx.InstallLabel: "mine"}},
			{ID: "bbbbbbbbbbbbbbbb", Labels: map[string]string{
				agent.LabelManaged: "true", agent.LabelInstance: "coder-theirs-1", dockerx.InstallLabel: "theirs"}},
			{ID: "cccccccccccccccc", Labels: map[string]string{
				agent.LabelManaged: "true", agent.LabelInstance: "coder-old-1"}},
		})
	}))
	t.Cleanup(engine.Close)
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+engine.Listener.Addr().String()), client.WithVersion("1.45"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	l := &taskLauncher{srv: &Server{docker: cli}}
	exited, err := l.ScanExitedTaskAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range exited {
		got = append(got, e.InstanceID)
	}
	if strings.Join(got, ",") != "coder-mine-1,coder-old-1" {
		t.Fatalf("scanned %v, want coder-mine-1 and coder-old-1", got)
	}
}
