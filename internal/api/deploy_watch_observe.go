package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/netguard"
	"github.com/MisterDonnuts/thulu/internal/project"
)

// deployWatchObserver is the deploy_watch step of a polled watch's automation. It reads the watch's
// endpoint or cloud with the daemon's own access (no agent, no run key), stores what it saw on the
// watch and reports whether that is a change: a success/failure transition or a new version. The
// automation opens a release task only when it is.
type deployWatchObserver struct {
	s      *Server
	client *http.Client
}

func newDeployWatchObserver(s *Server) *deployWatchObserver {
	return &deployWatchObserver{s: s, client: netguard.NewEgress().Client(deployWatchHTTPTimeout)}
}

const (
	deployWatchHTTPTimeout = 20 * time.Second
	deployWatchBodyLimit   = 64 << 10
	deployWatchCloudWindow = 24 * time.Hour
	deployWatchVersionMax  = 120
)

// The fields a health or version endpoint commonly answers its build with, in the order tried.
var deployVersionFields = []string{"version", "sha", "commit", "git_sha", "revision", "build"}

func (o *deployWatchObserver) Observe(ctx context.Context, workspaceID, watchID string) (map[string]any, error) {
	w, err := o.s.projectStore.GetDeployWatch(ctx, watchID)
	if err != nil {
		return nil, err
	}
	if w.WorkspaceID != workspaceID {
		return nil, project.ErrDeployWatchNotFound
	}
	var obs project.DeployObservation
	var detail string
	switch w.Source {
	case project.WatchHTTP:
		obs, detail = o.readHTTP(ctx, w.Target)
	case project.WatchCloud:
		obs, detail = o.readCloud(ctx, w)
	default:
		return nil, apperr.Invalid("api.deploy_watches.errors.not_polled", "source", w.Source)
	}
	obs.At = time.Now().UTC()
	changed := observationChanged(w, obs)
	if err := o.s.projectStore.RecordDeployObservation(ctx, w.ID, obs, changed); err != nil {
		return nil, err
	}
	if obs.Error != "" {
		return nil, apperr.Unavailable("api.deploy_watches.errors.read_failed", "target", w.Target, "reason", obs.Error)
	}
	return map[string]any{
		"changed":          changed,
		"state":            obs.State,
		"version":          obs.Version,
		"previous_state":   w.LastState,
		"previous_version": w.LastVersion,
		"fingerprint":      observationFingerprint(obs),
		"detail":           detail,
		"target":           w.Target,
	}, nil
}

// The first reading is the baseline. After it, a change of state is one, and so is a new version;
// a reading with no version (an endpoint that does not say it) keeps the last one.
func observationChanged(w *project.DeployWatch, obs project.DeployObservation) bool {
	if obs.Error != "" || w.LastState == "" {
		return false
	}
	return obs.State != w.LastState || (obs.Version != "" && obs.Version != w.LastVersion)
}

// The task fingerprint: one task per state and version the watch moves to.
func observationFingerprint(obs project.DeployObservation) string {
	v := obs.Version
	if v == "" {
		v = "-"
	}
	return obs.State + "-" + v
}

// An endpoint that answers 2xx is up; anything else, an unreachable host included, is a failure,
// which is what a broken deploy looks like from outside.
func (o *deployWatchObserver) readHTTP(ctx context.Context, url string) (project.DeployObservation, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return project.DeployObservation{Error: err.Error()}, ""
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return project.DeployObservation{State: project.WatchStateFailure},
			i18n.Text("api.deploy_watches.detail.unreachable", "reason", err.Error())
	}
	defer resp.Body.Close()
	obs := project.DeployObservation{State: project.WatchStateSuccess}
	// A body cut short still tells up from down by its status; it only loses the version.
	if body, readErr := io.ReadAll(io.LimitReader(resp.Body, deployWatchBodyLimit)); readErr == nil {
		obs.Version = versionFromBody(body)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		obs.State = project.WatchStateFailure
	}
	return obs, i18n.Text("api.deploy_watches.detail.http_status", "status", resp.StatusCode)
}

func versionFromBody(body []byte) string {
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	for _, f := range deployVersionFields {
		switch v := doc[f].(type) {
		case string:
			if v = strings.TrimSpace(v); v != "" {
				return clipVersion(v)
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

// Versions land in task titles and fingerprints; a long or multi-line one is clipped to one line.
func clipVersion(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	v = templateSafe(v)
	if len(v) > deployWatchVersionMax {
		v = v[:deployWatchVersionMax]
	}
	return v
}

// A cloud watch reads the environment's deploy markers through thulu-cloud with the credential the
// environment is granted, as the metrics collector does. The newest revision is the version; its
// state is success, since a revision Cloud Run lists is one it rolled out.
func (o *deployWatchObserver) readCloud(ctx context.Context, w *project.DeployWatch) (project.DeployObservation, string) {
	if o.s.envCollector == nil {
		return project.DeployObservation{Error: i18n.Text("api.deploy_watches.errors.cloud_off")}, ""
	}
	read, err := o.s.envCollector.RecentDeploys(ctx, w.Target, deployWatchCloudWindow)
	if err != nil {
		return project.DeployObservation{Error: apperr.LocalizeCtx(ctx, err)}, ""
	}
	if read.Unavailable {
		return project.DeployObservation{Error: read.Reason}, ""
	}
	if len(read.Deploys) == 0 {
		return project.DeployObservation{State: project.WatchStateSuccess, Version: w.LastVersion},
			i18n.Text("api.deploy_watches.detail.no_deploys")
	}
	deploys := read.Deploys
	sort.SliceStable(deploys, func(i, j int) bool { return deploys[i].At > deploys[j].At })
	latest := deploys[0]
	version := latest.Revision
	if version == "" {
		version = latest.Image
	}
	return project.DeployObservation{State: project.WatchStateSuccess, Version: clipVersion(version)},
		i18n.Text("api.deploy_watches.detail.cloud_deploy", "service", latest.Service, "at", latest.At)
}
