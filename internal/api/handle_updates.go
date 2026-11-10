package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/changepkg"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kept"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/semver"
	"github.com/MisterDonnuts/thulu/internal/updatecheck"
)

const (
	// The source may be api.github.com, which rate-limits per address.
	updateCheckEvery = time.Minute
	updateCheckWait  = 30 * time.Second

	maxUpdateEventBody = 16 << 10
	maxUpdateOperator  = 200
	maxUpdateDetail    = 4000

	modeDocker     = changepkg.ModeDocker
	modeKubernetes = changepkg.ModeKubernetes
)

type updateDeps struct {
	checker *updatecheck.Checker

	mu         sync.Mutex
	lastManual time.Time
}

func (s *Server) SetUpdateChecker(c *updatecheck.Checker) { s.updates.checker = c }

func (s *Server) updatesOperator(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireOperator(w, r,
			i18n.TCtx(r.Context(), "api.updates.labels.operator_subject"),
			i18n.TCtx(r.Context(), "api.updates.labels.operator_verb")) {
			return
		}
		h(w, r)
	}
}

func (s *Server) handleUpdatesNotFound(w http.ResponseWriter, r *http.Request) {
	writeErrKey(w, r, http.StatusNotFound, "api.updates.errors.no_such_route")
}

func (s *Server) updateMode() string {
	if s.kube != nil || strings.EqualFold(strings.TrimSpace(os.Getenv("OCTOPUS_AGENT_RUNTIME")), modeKubernetes) {
		return modeKubernetes
	}
	return modeDocker
}

func (s *Server) changePackage(st updatecheck.State, rel updatecheck.Release, locale string) *changepkg.Package {
	from := st.Current
	if _, ok := semver.Parse(from); !ok {
		from = ""
	}
	mode := s.updateMode()
	f := changepkg.Facts{From: from, Mode: mode, Source: st.Source, Notes: rel.Notes, Locale: locale}
	// In Docker the daemon gets deploy/.env whole (env_file), so a missing variable is missing from
	// the install.
	// In Kubernetes the new chart may set it, so it is not checked.
	if mode == modeDocker {
		f.MissingEnv = []string{}
		for _, k := range rel.Manifest.Requires {
			if strings.TrimSpace(os.Getenv(k)) == "" {
				f.MissingEnv = append(f.MissingEnv, k)
			}
		}
	}
	return changepkg.Build(rel.Manifest, f)
}

func applyCommand(p *changepkg.Package) string {
	for _, c := range p.Plan {
		if strings.HasPrefix(c, "thulu update -version ") { //i18n:ignore shell command
			return c
		}
	}
	return "thulu update -version " + p.To //i18n:ignore shell command
}

type updateAdvisoryView struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	URL      string `json:"url,omitempty"`
	FixedIn  string `json:"fixed_in"`
}

type updateReleaseView struct {
	Version     string     `json:"version"`
	Channel     string     `json:"channel"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	// Notes is unsigned markdown from the source; the UI must render it through its sanitizing
	// renderer.
	Notes       string                `json:"notes,omitempty"`
	NotesURL    string                `json:"notes_url,omitempty"`
	Commit      string                `json:"commit"`
	Migrations  release.Migrations    `json:"migrations"`
	Requires    []string              `json:"requires"`
	ManualSteps []string              `json:"manual_steps"`
	Advisories  []updateAdvisoryView  `json:"advisories"`
	UpgradeFrom string                `json:"upgrade_from"`
	CanUpgrade  bool                  `json:"can_upgrade"`
	Rollback    updateReleaseRollback `json:"rollback"`
	Blockers    []string              `json:"blockers"`
	Unverified  string                `json:"unverified,omitempty"`
	Command     string                `json:"command"`
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

type updateReleaseRollback struct {
	To             string `json:"to"`
	WithoutRestore bool   `json:"without_restore"`
}

type updateStatusView struct {
	Version  string `json:"version"`
	DevBuild bool   `json:"dev_build"`
	Channel  string `json:"channel"`
	Mode     string `json:"mode"`

	Source      string   `json:"source"`
	SourceOff   bool     `json:"source_off"`
	SourceError string   `json:"source_error,omitempty"`
	Channels    []string `json:"channels"`
	IntervalSec int      `json:"interval_seconds"`

	LastCheck *time.Time `json:"last_check"`
	LastError string     `json:"last_error,omitempty"`

	UpdateAvailable bool                    `json:"update_available"`
	Advisories      []updateAdvisoryView    `json:"advisories"`
	Available       []updateReleaseView     `json:"available"`
	Rejected        []updatecheck.Rejection `json:"rejected"`
	RollbackTo      string                  `json:"rollback_to,omitempty"`
}

func (s *Server) handleUpdatesStatus(w http.ResponseWriter, r *http.Request) {
	c := s.updates.checker
	if c == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.updates.errors.checker_not_configured")
		return
	}
	writeJSON(w, http.StatusOK, s.updateStatus(r.Context(), c.State()))
}

func (s *Server) updateStatus(ctx context.Context, st updatecheck.State) updateStatusView {
	v := updateStatusView{
		Version:     st.Current,
		DevBuild:    st.DevBuild,
		Channel:     currentChannel(st),
		Mode:        s.updateMode(),
		Source:      st.Source,
		SourceOff:   st.Off,
		SourceError: st.ConfigError,
		Channels:    nonNil(st.Channels),
		IntervalSec: int(st.Interval / time.Second),
		LastError:   st.LastError,
		Advisories:  []updateAdvisoryView{},
		Available:   []updateReleaseView{},
		Rejected:    nonNil(st.Rejected),
	}
	if !st.LastCheck.IsZero() {
		t := st.LastCheck
		v.LastCheck = &t
	}
	if st.CurrentManifest != nil {
		v.RollbackTo = st.CurrentManifest.RollbackTo
	}
	// Iterate oldest first so FixedIn names the first release carrying the fix, not the newest.
	fixed := map[string]bool{}
	for i := len(st.Releases) - 1; i >= 0; i-- {
		r := st.Releases[i]
		if !r.Verified() {
			// Unverified manifest: show only what the source listed; the command verifies the build
			// before installing.
			v.Available = append([]updateReleaseView{{
				Version:     r.Version,
				Channel:     r.Channel,
				PublishedAt: timeOrNil(r.PublishedAt),
				Notes:       r.Notes,
				Requires:    []string{},
				ManualSteps: []string{},
				Advisories:  []updateAdvisoryView{},
				Blockers:    []string{},
				Unverified:  i18n.TCtx(ctx, "api.updates.hints.unverified_preview"),
				Command:     "thulu update -version " + r.Version, //i18n:ignore shell command
			}}, v.Available...)
			continue
		}
		m := r.Manifest
		pkg := s.changePackage(st, r, i18n.FromContext(ctx))
		view := updateReleaseView{
			Version:     r.Version,
			Channel:     r.Channel,
			PublishedAt: timeOrNil(r.PublishedAt),
			Notes:       r.Notes,
			NotesURL:    m.NotesURL,
			Commit:      m.Commit,
			Migrations:  m.Migrations,
			Requires:    nonNil(m.Requires),
			ManualSteps: nonNil(m.ManualSteps),
			Advisories:  []updateAdvisoryView{},
			UpgradeFrom: m.UpgradeFrom,
			CanUpgrade:  m.CanUpgradeFrom(st.Current),
			Rollback: updateReleaseRollback{
				To:             st.Current,
				WithoutRestore: m.RollbackWithoutRestore(st.Current),
			},
			Blockers: nonNil(pkg.Blockers),
			Command:  applyCommand(pkg),
		}
		for _, a := range m.AdvisoriesFor(st.Current) {
			av := updateAdvisoryView{ID: a.ID, Severity: a.Severity, Summary: a.Summary, URL: a.URL, FixedIn: r.Version}
			view.Advisories = append(view.Advisories, av)
			if !fixed[a.ID] {
				fixed[a.ID] = true
				v.Advisories = append(v.Advisories, av)
			}
		}
		v.Available = append([]updateReleaseView{view}, v.Available...)
	}
	_, parses := semver.Parse(st.Current)
	v.UpdateAvailable = parses && len(v.Available) > 0
	return v
}

func currentChannel(st updatecheck.State) string {
	if st.CurrentManifest != nil {
		return st.CurrentManifest.Channel
	}
	if st.DevBuild {
		return "development"
	}
	if v, ok := semver.Parse(st.Current); ok && v.Prerelease() {
		return release.ChannelPrerelease
	}
	return release.ChannelStable
}

func (s *Server) handleUpdatesCheck(w http.ResponseWriter, r *http.Request) {
	c := s.updates.checker
	if c == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.updates.errors.checker_not_configured")
		return
	}
	if !c.Enabled() {
		st := c.State()
		if st.ConfigError != "" {
			writeErrKey(w, r, http.StatusConflict, "api.updates.errors.source_misconfigured", "err", st.ConfigError)
			return
		}
		writeErrKey(w, r, http.StatusConflict, "api.updates.errors.lookups_off", "env", release.EnvSource)
		return
	}
	if wait := s.reserveManualCheck(time.Now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second)/time.Second)+1))
		writeErrKey(w, r, http.StatusTooManyRequests, "api.updates.errors.check_too_soon", "wait", wait.Round(time.Second).String())
		return
	}
	if s.auditStore != nil {
		s.auditStore.LogRequest(r, audit.KindUpdateChecked, "", "install", i18n.Text("api.updates.audit.checked"), map[string]string{
			"source": c.State().Source,
		})
	}

	// The check outlives the request: the request deadline is shorter than a slow source, and a cut
	// check wastes the rate-limited call.
	done := make(chan error, 1)
	go func() { done <- c.Check(context.WithoutCancel(r.Context())) }()
	select {
	case <-done:
		writeJSON(w, http.StatusOK, s.updateStatus(r.Context(), c.State()))
	case <-time.After(updateCheckWait):
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "checking"})
	case <-r.Context().Done():
	}
}

func (s *Server) reserveManualCheck(now time.Time) time.Duration {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if next := s.updates.lastManual.Add(updateCheckEvery); now.Before(next) {
		return next.Sub(now)
	}
	s.updates.lastManual = now
	return 0
}

var unsafeFilename = regexp.MustCompile(`[^0-9A-Za-z.+_-]`)

func (s *Server) handleUpdatesPackage(w http.ResponseWriter, r *http.Request) {
	c := s.updates.checker
	if c == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.updates.errors.checker_not_configured")
		return
	}
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "json" {
		writeErrKey(w, r, http.StatusBadRequest, "api.updates.errors.bad_format")
		return
	}
	if to == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.updates.errors.to_required")
		return
	}
	// Only a release the last check verified: the package is built from its signed manifest, and
	// nothing the request sent reaches the body.
	st := c.State()
	var rel updatecheck.Release
	for _, offered := range st.Releases {
		if offered.Version == to && offered.Verified() {
			rel = offered
		}
	}
	if rel.Manifest == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.updates.errors.no_verified_release", "version", to)
		return
	}
	to = rel.Version
	current := st.Current
	pkg := s.changePackage(st, rel, i18n.RequestLocale(r))
	body := pkg.Markdown()
	contentType := "text/markdown; charset=utf-8" //i18n:ignore HTTP content type
	if format == "json" {
		b, err := pkg.JSON()
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.updates.errors.render_package_failed"))
			return
		}
		body, contentType = b, "application/json"
	}

	if s.auditStore != nil {
		s.auditStore.LogRequest(r, audit.KindUpdatePackage, to, "install",
			i18n.Text("api.updates.audit.package_downloaded", "version", to), map[string]string{
				"from": current, "to": to, "format": format,
			})
	}
	name := unsafeFilename.ReplaceAllString("octopus-"+current+"-to-"+to, "_") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`) //i18n:ignore HTTP header value
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type keptVersionView struct {
	Version         string     `json:"version"`
	Current         bool       `json:"current"`
	Since           *time.Time `json:"since,omitempty"`
	Until           *time.Time `json:"until,omitempty"`
	WithoutRestore  bool       `json:"without_restore"`
	Snapshot        string     `json:"snapshot,omitempty"`
	SnapshotMissing bool       `json:"snapshot_missing,omitempty"`
	Command         string     `json:"command,omitempty"`
}

type keptVersionsView struct {
	Mode          string            `json:"mode"`
	Versions      []keptVersionView `json:"versions"`
	RollbackKnown bool              `json:"rollback_known"`
}

func (s *Server) handleUpdatesVersions(w http.ResponseWriter, r *http.Request) {
	mode := s.updateMode()
	out := keptVersionsView{Mode: mode, Versions: []keptVersionView{}}
	if mode == modeKubernetes {
		writeJSON(w, http.StatusOK, out)
		return
	}
	current := buildinfo.Version
	rollbackTo := ""
	if s.updates.checker != nil {
		if m := s.updates.checker.State().CurrentManifest; m != nil {
			rollbackTo = m.RollbackTo
			out.RollbackKnown = true
		}
	}
	var history []audit.Entry
	if s.auditStore != nil {
		h, err := s.auditStore.UpdateHistory(r.Context(), 1000)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.updates.errors.read_history_failed"))
			return
		}
		history = h
	}
	spans := versionSpans(history)
	// Lists written by an older CLI (or none) may lack the running version.
	versions := kept.FromEnv(os.Getenv)
	if !slices.ContainsFunc(versions, func(v kept.Version) bool { return v.Version == current }) {
		versions = append([]kept.Version{{Version: current}}, versions...)
	}
	for _, v := range versions {
		row := keptVersionView{Version: v.Version, Current: v.Version == current}
		if sp, ok := spans[v.Version]; ok {
			row.Since = sp.since
			if !row.Current {
				row.Until = sp.until
			}
		}
		if !row.Current {
			row.WithoutRestore = rollbackTo != "" && v.Version == rollbackTo
			row.Snapshot = v.Snapshot
			row.SnapshotMissing = !row.WithoutRestore && v.Snapshot == ""
			row.Command = kept.Command(v, current, row.WithoutRestore)
		}
		out.Versions = append(out.Versions, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// Versions from CLI reports end up in commands and filenames: a semver or build stamp only, never a
// path or shell word.
var versionToken = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)

type versionSpan struct{ since, until *time.Time }

func versionSpans(history []audit.Entry) map[string]versionSpan {
	var changes []*audit.Entry
	for i := range history {
		if history[i].Kind == audit.KindVersionChanged {
			changes = append(changes, &history[i])
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].CreatedAt.Before(changes[j].CreatedAt) })
	out := map[string]versionSpan{}
	for i, e := range changes {
		to := e.Metadata["to"]
		if to == "" {
			continue
		}
		since := e.CreatedAt
		sp := versionSpan{since: &since}
		if i+1 < len(changes) {
			until := changes[i+1].CreatedAt
			sp.until = &until
		}
		out[to] = sp
	}
	return out
}

type updateHistoryView struct {
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Result     string    `json:"result,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	Operator   string    `json:"operator,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Image      string    `json:"image,omitempty"`
	StartedAt  string    `json:"started_at,omitempty"`
	FinishedAt string    `json:"finished_at,omitempty"`
	Actor      string    `json:"actor"`
	ActorType  string    `json:"actor_type,omitempty"`
}

func (s *Server) handleUpdatesHistory(w http.ResponseWriter, r *http.Request) {
	if s.auditStore == nil {
		writeJSON(w, http.StatusOK, []updateHistoryView{})
		return
	}
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	entries, err := s.auditStore.UpdateHistory(r.Context(), limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.updates.errors.read_history_failed"))
		return
	}
	out := make([]updateHistoryView, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		m := e.Metadata
		actor := e.ActorLabel
		if actor == "" {
			actor = e.ActorEmail
		}
		if actor == "" {
			actor = e.ActorID
		}
		out = append(out, updateHistoryView{
			At: e.CreatedAt, Kind: string(e.Kind),
			From: m["from"], To: m["to"], Result: m["result"], Mode: m["mode"],
			Operator: m["operator"], Detail: m["detail"], Image: m["image"],
			StartedAt: m["started_at"], FinishedAt: m["finished_at"],
			Actor: actor, ActorType: e.ActorType,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type updateEvent struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Result     string `json:"result"`
	Mode       string `json:"mode"`
	Operator   string `json:"operator"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Detail     string `json:"detail"`
}

// The labels are read back by auditValues; keep both in sync.
var updateResults = map[string]string{
	"ok":          "api.updates.labels.result_ok",
	"failed":      "api.updates.labels.result_failed",
	"rolled_back": "api.updates.labels.result_rolled_back",
}

func (e *updateEvent) validate(now time.Time) error {
	if e.From != "" && !versionToken.MatchString(e.From) {
		return apperr.Invalid("api.updates.errors.from_not_version")
	}
	if !versionToken.MatchString(e.To) {
		return apperr.Invalid("api.updates.errors.to_not_version")
	}
	if _, ok := updateResults[e.Result]; !ok {
		return apperr.Invalid("api.updates.errors.bad_result")
	}
	if e.Mode != modeDocker && e.Mode != modeKubernetes {
		return apperr.Invalid("api.updates.errors.bad_mode")
	}
	if len(e.Operator) > maxUpdateOperator || !printable(e.Operator, false) {
		return apperr.Invalid("api.updates.errors.bad_operator", "max", maxUpdateOperator)
	}
	if len(e.Detail) > maxUpdateDetail || !printable(e.Detail, true) {
		return apperr.Invalid("api.updates.errors.bad_detail", "max", maxUpdateDetail)
	}
	started, err := time.Parse(time.RFC3339, e.StartedAt)
	if err != nil {
		return apperr.Invalid("api.updates.errors.bad_started_at")
	}
	finished, err := time.Parse(time.RFC3339, e.FinishedAt)
	if err != nil {
		return apperr.Invalid("api.updates.errors.bad_finished_at")
	}
	if finished.Before(started) {
		return apperr.Invalid("api.updates.errors.finished_before_started")
	}
	if finished.After(now.Add(time.Hour)) {
		return apperr.Invalid("api.updates.errors.finished_in_future")
	}
	return nil
}

// Control characters would let a report forge extra lines in a log or CSV export.
func printable(s string, multiline bool) bool {
	for _, r := range s {
		if multiline && (r == '\n' || r == '\t') {
			continue
		}
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Server) handleUpdatesEvent(w http.ResponseWriter, r *http.Request) {
	if !auth.IsAPIKeyAuthed(r.Context()) {
		writeErrKey(w, r, http.StatusForbidden, "api.updates.errors.report_needs_operator_key")
		return
	}
	if s.auditStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.updates.errors.audit_not_configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpdateEventBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var e updateEvent
	if err := dec.Decode(&e); err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.updates.errors.invalid_body"))
		return
	}
	if err := e.validate(time.Now()); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	from := e.From
	if from == "" {
		from = i18n.Text("api.updates.labels.unknown_version")
	}
	s.auditStore.LogRequest(r, audit.KindUpdateReported, e.To, "install",
		i18n.Text("api.updates.audit.reported", "from", from, "to", e.To, "result", e.Result),
		map[string]string{
			"from": e.From, "to": e.To, "result": e.Result, "mode": e.Mode,
			"operator": e.Operator, "started_at": e.StartedAt, "finished_at": e.FinishedAt,
			"detail": e.Detail,
		})
	writeJSON(w, http.StatusCreated, map[string]string{"status": "recorded"})
}
