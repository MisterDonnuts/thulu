package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type cloudTargetsStub struct{}

func (cloudTargetsStub) ResolveCollectTarget(context.Context, string) (*envmetrics.CollectTarget, error) {
	return &envmetrics.CollectTarget{}, nil
}

func (cloudTargetsStub) ResolveDeployTarget(context.Context, string) (*envmetrics.CollectTarget, error) {
	return &envmetrics.CollectTarget{}, nil
}

func TestObservationChanged(t *testing.T) {
	watch := func(state, version string) *project.DeployWatch {
		return &project.DeployWatch{LastState: state, LastVersion: version}
	}
	ok, fail := project.WatchStateSuccess, project.WatchStateFailure
	for name, c := range map[string]struct {
		w    *project.DeployWatch
		obs  project.DeployObservation
		want bool
	}{
		"first reading is the baseline": {watch("", ""), project.DeployObservation{State: ok, Version: "v1"}, false},
		"same reading":                  {watch(ok, "v1"), project.DeployObservation{State: ok, Version: "v1"}, false},
		"new version":                   {watch(ok, "v1"), project.DeployObservation{State: ok, Version: "v2"}, true},
		"success to failure":            {watch(ok, "v1"), project.DeployObservation{State: fail, Version: "v1"}, true},
		"failure back to success":       {watch(fail, "v1"), project.DeployObservation{State: ok}, true},
		"no version keeps the last":     {watch(ok, "v1"), project.DeployObservation{State: ok}, false},
		"a read error is no change":     {watch(ok, "v1"), project.DeployObservation{Error: "timeout"}, false},
	} {
		if got := observationChanged(c.w, c.obs); got != c.want {
			t.Errorf("%s = %v, want %v", name, got, c.want)
		}
	}
}

func TestVersionFromBody(t *testing.T) {
	for body, want := range map[string]string{
		`{"version":"1.4.2","sha":"abc"}`:   "1.4.2",
		`{"commit":"9f2c1e7"}`:              "9f2c1e7",
		`{"build":512}`:                     "512",
		`{"version":"{{ steps.x }}"}`:       "(( steps.x ))",
		`{"status":"ok"}`:                   "",
		`<html>ok</html>`:                   "",
		`{"version":"  two\nlines  here "}`: "two lines here",
	} {
		if got := versionFromBody([]byte(body)); got != want {
			t.Errorf("versionFromBody(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestReadCloudTakesTheNewestRevision(t *testing.T) {
	var answer map[string]any
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(cloud.Close)
	c := envmetrics.NewCollector(nil, nil, cloud.URL)
	c.SetTargets(cloudTargetsStub{})
	o := &deployWatchObserver{s: &Server{envCollector: c}}
	w := &project.DeployWatch{Source: project.WatchCloud, Target: "env1", LastState: project.WatchStateSuccess, LastVersion: "api-00041"}

	answer = map[string]any{"deploys": []any{
		map[string]any{"at": "2026-11-18T09:00:00Z", "service": "api", "revision": "api-00041"},
		map[string]any{"at": "2026-11-18T10:00:00Z", "service": "api", "revision": "api-00042"},
	}}
	if obs, _ := o.readCloud(t.Context(), w); obs.Error != "" || obs.Version != "api-00042" || obs.State != project.WatchStateSuccess {
		t.Errorf("two deploys read as %+v", obs)
	}
	answer = map[string]any{"deploys": []any{}}
	if obs, _ := o.readCloud(t.Context(), w); obs.Version != "api-00041" {
		t.Errorf("a quiet day read as %+v, want the last version kept", obs)
	}
	answer = map[string]any{"deploys": []any{}, "unavailable": true, "reason": "aws has no deploy markers yet"}
	if obs, _ := o.readCloud(t.Context(), w); obs.Error == "" {
		t.Errorf("an unavailable provider read as %+v", obs)
	}
	if obs, _ := (&deployWatchObserver{s: &Server{}}).readCloud(t.Context(), w); obs.Error == "" {
		t.Errorf("no collector read as %+v", obs)
	}
}
