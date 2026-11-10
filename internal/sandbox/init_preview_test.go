package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
)

type publishedRuntime struct {
	*localRuntime
	ports map[int]int
}

func (p *publishedRuntime) Create(ctx context.Context, opts CreateOpts) (*CreateResult, error) {
	res, err := p.localRuntime.Create(ctx, opts)
	if err != nil {
		return nil, err
	}
	res.HostPorts = p.ports
	return res, nil
}

type peerRuntime struct{ *publishedRuntime }

func (p *peerRuntime) PeerAddress(_ context.Context, containerID string, port int) (string, bool) {
	return fmt.Sprintf("http://%s:%d", containerID, port), true
}

type clusterRuntime struct{ *publishedRuntime }

func (c *clusterRuntime) ClusterAddress(_ context.Context, containerID string, port int) (string, bool) {
	if port == 8080 {
		return "", false
	}
	return fmt.Sprintf("http://%s.tenant-ws-1.svc:%d", containerID, port), true
}

func TestInitRun_ExportsThePreviewURLs(t *testing.T) {
	published := map[int]int{3000: 32001, 5173: 32002, 8000: 32003, 8080: 32004}
	cases := []struct {
		name    string
		wrap    func(*localRuntime) Runtime
		missing []int
	}{
		{"docker, default bridge", func(l *localRuntime) Runtime {
			return &publishedRuntime{localRuntime: l, ports: published}
		}, nil},
		{"docker, agent network", func(l *localRuntime) Runtime {
			return &peerRuntime{&publishedRuntime{localRuntime: l, ports: published}}
		}, nil},
		{"kubernetes", func(l *localRuntime) Runtime {
			return &clusterRuntime{&publishedRuntime{localRuntime: l, ports: published}}
		}, []int{8080}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newInitRigOn(t, initScript(`env | grep '^OCTOPUS_PREVIEW_URL_' | sort`, project.InitManifest{}), tc.wrap)
			var res InitRunResult
			if text, isErr := r.call(t, "init_run", `{"project":"app"}`, &res); isErr || res.State != InitStateReady {
				t.Fatalf("init_run = %s", text)
			}
			envFile, err := os.ReadFile(filepath.Join(initRoot, "app", "env"))
			if err != nil {
				t.Fatal(err)
			}
			for port := range published {
				name := fmt.Sprintf("OCTOPUS_PREVIEW_URL_%d", port)
				info, perr := r.h.mgr.PreviewURL(t.Context(), "task-1", port)
				if slices.Contains(tc.missing, port) {
					if perr == nil {
						t.Fatalf("port %d: preview_url answered %q, the case expects no address", port, info.URL)
					}
					if strings.Contains(string(envFile), name+"=") {
						t.Errorf("env file exports %s with no address behind it:\n%s", name, envFile)
					}
					continue
				}
				if perr != nil {
					t.Fatalf("port %d: preview_url: %v", port, perr)
				}
				if want := "export " + name + "=" + shellSingleQuote(info.URL) + "\n"; !strings.Contains(string(envFile), want) {
					t.Errorf("env file lacks %q:\n%s", want, envFile)
				}
				if want := name + "=" + info.URL; !strings.Contains(res.LogTail, want) {
					t.Errorf("the script did not see %q; log:\n%s", want, res.LogTail)
				}
			}
		})
	}
}
