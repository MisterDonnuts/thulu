package evidence_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newStore(t *testing.T) *evidence.Store {
	t.Helper()
	s, _ := newStores(t)
	return s
}

// Open the project store first, as the daemon does: evidence migration 2 reads the project tables.
func newStores(t *testing.T) (*evidence.Store, *project.Store) {
	t.Helper()
	db := testpg.New(t)
	ps, err := project.NewStore(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := evidence.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return s, ps
}

func mustProject(t *testing.T, db *sql.DB, ws, id, name, repo string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO projects (id, name, path, repo_url, default_branch, workspace_id, created_at)
		VALUES ($1, $2, $3, $4, 'main', $5, NOW())`, id, name, "/data/projects/"+id, repo, ws); err != nil {
		t.Fatal(err)
	}
}

func mustProjectEnv(t *testing.T, s *evidence.Store, ps *project.Store, ws, projectID string, in project.EnvironmentInput) *evidence.Environment {
	t.Helper()
	ctx := context.Background()
	pe, err := ps.CreateEnvironment(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureProjectRows(ctx, ws); err != nil {
		t.Fatal(err)
	}
	envs, err := s.ListEnvironments(ctx, ws, projectID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range envs {
		if envs[i].ProjectEnvironmentID == pe.ID {
			return &envs[i]
		}
	}
	t.Fatalf("no evidence row for project environment %s", pe.ID)
	return nil
}

func sandboxEnv(t *testing.T, s *evidence.Store, ws, projectID string) *evidence.Environment {
	t.Helper()
	e, err := s.SandboxEnvironment(context.Background(), ws, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

const take = `{"version":1,"title":"Login","base_url":"https://app.example.com","steps":[{"kind":"navigate","url":"/"}]}`

func mustTake(t *testing.T) json.RawMessage {
	t.Helper()
	tk, err := evidence.ValidateTake([]byte(take))
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func mustEnv(t *testing.T, s *evidence.Store, ws, name string) *evidence.Environment {
	t.Helper()
	e, err := s.CreateEnvironment(context.Background(), ws, "ana@example.com", evidence.Environment{Name: name, BaseURL: "https://staging.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCaseKeysCountPerWorkspace(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i, want := range []string{"EV-1", "EV-2"} {
		c, err := s.CreateCase(ctx, "ws-a", "ana", evidence.Case{Title: "Case " + want})
		if err != nil {
			t.Fatal(err)
		}
		if c.Key != want || c.Status != evidence.CaseDraft {
			t.Fatalf("case %d: key %s status %s", i, c.Key, c.Status)
		}
	}
	other, err := s.CreateCase(ctx, "ws-b", "bo", evidence.Case{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Key != "EV-1" {
		t.Fatalf("second workspace starts at EV-1, got %s", other.Key)
	}
	if _, err := s.GetCase(ctx, "ws-b", "EV-2", false); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("ws-b must not see ws-a's EV-2: %v", err)
	}
	cases, counts, err := s.ListCases(ctx, evidence.CaseFilter{WorkspaceID: "ws-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 || cases[0].Key != "EV-2" || counts[evidence.CaseDraft] != 2 {
		t.Fatalf("list: %+v counts %v", cases, counts)
	}
}

func TestCaseValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "  "}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("empty title: %v", err)
	}
	if _, err := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "x", Priority: "urgent"}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("bad priority: %v", err)
	}
	c, err := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "x", Tags: []string{"Login", "login", " ", "smoke"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Tags, ",") != "Login,smoke" {
		t.Fatalf("tags not normalized: %v", c.Tags)
	}
}

func TestValidateTake(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":  `{`,
		"version":   `{"version":2,"base_url":"https://a.b","steps":[{}]}`,
		"no url":    `{"version":1,"steps":[{}]}`,
		"no steps":  `{"version":1,"base_url":"https://a.b","steps":[]}`,
		"ftp url":   `{"version":1,"base_url":"ftp://a.b","steps":[{}]}`,
		"too large": `{"version":1,"base_url":"https://a.b","steps":[{}],"pad":"` + strings.Repeat("x", 1<<20) + `"}`,
	} {
		if _, err := evidence.ValidateTake([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := evidence.ValidateTake([]byte(take)); err != nil {
		t.Fatal(err)
	}
}

func TestTakeMovesStatus(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c, _ := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "x"})
	c, err := s.SetTake(ctx, "ws", c.Key, "ana", mustTake(t), evidence.SourceUpload)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != evidence.CaseReady || !c.HasTake || c.TakeSource != evidence.SourceUpload || len(c.Take) == 0 {
		t.Fatalf("after take: %+v", c)
	}
	c, err = s.SetTake(ctx, "ws", c.ID, "ana", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != evidence.CaseDraft || c.HasTake {
		t.Fatalf("after clearing: %+v", c)
	}
}

func TestTakeSavedAgainIsNotDraft(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c, _ := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "x", Take: mustTake(t), TakeSource: evidence.SourceUpload})
	if c.Status != evidence.CaseReady {
		t.Fatalf("created with a take: status %s, want ready", c.Status)
	}
	if _, err := s.DB().Exec(`UPDATE evidence_cases SET last_run_at = now(), status = 'pass' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTake(ctx, "ws", c.ID, "ana", nil, ""); err != nil {
		t.Fatal(err)
	}
	c, err := s.SetTake(ctx, "ws", c.ID, "ana", mustTake(t), evidence.SourceAgent)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != evidence.CaseReady {
		t.Fatalf("recorded again: status %s, want ready", c.Status)
	}
	if _, err = s.DB().Exec(`UPDATE evidence_cases SET status = 'draft' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	list, counts, err := s.ListCases(ctx, evidence.CaseFilter{WorkspaceID: "ws", Statuses: []string{evidence.CaseReady}})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != evidence.CaseReady || counts[evidence.CaseReady] != 1 || counts[evidence.CaseDraft] != 0 {
		t.Fatalf("listing: %d cases, counts %v", len(list), counts)
	}
}

func TestEnvironmentSecretsAreSealedAndNamed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	e := mustEnv(t, s, "ws", "Staging")
	if e.BaseURL != "https://staging.example.com" {
		t.Fatalf("trailing slash kept: %s", e.BaseURL)
	}
	if _, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "Staging", BaseURL: "https://x.y"}); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "Bad", BaseURL: "https://u:p@x.y"}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("userinfo accepted: %v", err)
	}
	pw := "hunter2"
	e, err := s.SetSecrets(ctx, "ws", e.ID, map[string]*string{"TEST_PASSWORD": &pw, "TEST_USER": strPtr("qa@example.com")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.SecretNames, ",") != "TEST_PASSWORD,TEST_USER" {
		t.Fatalf("names: %v", e.SecretNames)
	}
	var sealed string
	if err := s.DB().QueryRow(`SELECT secrets_sealed FROM evidence_environments WHERE id = $1`, e.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, pw) {
		t.Fatal("secret stored in clear")
	}
	got, err := s.Secrets(ctx, "ws", e.ID)
	if err != nil || got["TEST_PASSWORD"] != pw {
		t.Fatalf("secrets: %v %v", got, err)
	}
	if _, err := s.Secrets(ctx, "other", e.ID); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("other workspace read secrets: %v", err)
	}
	e, err = s.SetSecrets(ctx, "ws", e.ID, map[string]*string{"TEST_USER": nil})
	if err != nil || strings.Join(e.SecretNames, ",") != "TEST_PASSWORD" {
		t.Fatalf("remove: %v %v", e.SecretNames, err)
	}
	if _, err := s.SetSecrets(ctx, "ws", e.ID, map[string]*string{"lower": &pw}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("bad name accepted: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func recorded(t *testing.T, s *evidence.Store, ws string, n int) []*evidence.Case {
	t.Helper()
	ctx := context.Background()
	var out []*evidence.Case
	for i := 0; i < n; i++ {
		c, err := s.CreateCase(ctx, ws, "ana", evidence.Case{Title: "Recorded", Take: mustTake(t), TakeSource: evidence.SourceUpload})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if _, err := s.CreateCase(ctx, ws, "ana", evidence.Case{Title: "Draft"}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBatchRunsOnlyRecordedCases(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	e := mustEnv(t, s, "ws", "Staging")
	if _, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID}); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Fatalf("empty library: %v", err)
	}
	cases := recorded(t, s, "ws", 2)
	b, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if b.Total != 2 || len(b.Runs) != 2 || b.Runs[0].CaseKey != cases[0].Key || b.Status != evidence.BatchQueued {
		t.Fatalf("batch: %+v", b)
	}
	one, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID, CaseIDs: []string{cases[1].Key}})
	if err != nil || one.Total != 1 {
		t.Fatalf("narrowed batch: %+v %v", one, err)
	}
	if _, err := s.CreateBatch(ctx, "other", "bo", evidence.BatchSpec{EnvironmentID: e.ID}); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("other workspace used the environment: %v", err)
	}
}

func TestRunnerFlow(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	e := mustEnv(t, s, "ws", "Staging")
	cases := recorded(t, s, "ws", 2)
	b, _ := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID})

	if run, err := s.ClaimNextRun(ctx, "ws", b.ID); err != nil || run != nil {
		t.Fatalf("a queued batch hands out nothing: %v %v", run, err)
	}
	if ok, err := s.StartBatch(ctx, b.ID, "pod-1"); !ok || err != nil {
		t.Fatalf("start: %v %v", ok, err)
	}
	if _, err := s.ClaimNextRun(ctx, "other", b.ID); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("another workspace claimed: %v", err)
	}
	run, err := s.ClaimNextRun(ctx, "ws", b.ID)
	if err != nil || run == nil || run.CaseID != cases[0].ID || run.Status != evidence.RunRunning || len(run.Take) == 0 {
		t.Fatalf("claim: %+v %v", run, err)
	}
	a, err := s.AddArtifact(ctx, evidence.Artifact{WorkspaceID: "ws", RunID: run.ID, Kind: evidence.ArtifactVideo,
		Filename: "take.mp4", MimeType: "video/mp4", Size: 1234, Path: "/tmp/x.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, "ws", b.ID, run.ID, evidence.RunResult{Status: evidence.RunPass, VideoID: "nope"}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("foreign artifact id accepted: %v", err)
	}
	done, err := s.FinishRun(ctx, "ws", b.ID, run.ID, evidence.RunResult{Status: evidence.RunPass, VideoID: a.ID,
		Checks: []evidence.Check{{Label: "Logged in", Pass: true, Measured: true}}, DurationMS: 4200})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != evidence.RunPass || done.VideoID != a.ID || len(done.Artifacts) != 1 || len(done.Checks) != 1 {
		t.Fatalf("finished: %+v", done)
	}
	if _, err := s.FinishRun(ctx, "ws", b.ID, run.ID, evidence.RunResult{Status: evidence.RunFail}); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("finished twice: %v", err)
	}
	c, _ := s.GetCase(ctx, "ws", cases[0].ID, false)
	if c.Status != evidence.RunPass || c.LastRunAt == nil {
		t.Fatalf("case after pass: %+v", c)
	}
	if _, err := s.ClaimNextRun(ctx, "ws", b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseBatch(ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	b, _ = s.GetBatch(ctx, "ws", b.ID)
	if b.Status != evidence.BatchDone || b.Passed != 1 || b.Errored != 1 || b.FinishedAt == nil {
		t.Fatalf("closed batch: %+v", b)
	}
	c, _ = s.GetCase(ctx, "ws", cases[1].ID, false)
	if c.Status != evidence.RunError {
		t.Fatalf("case of the lost run: %s", c.Status)
	}
	cases2, _, _ := s.ListCases(ctx, evidence.CaseFilter{WorkspaceID: "ws", EnvironmentID: e.ID, Statuses: []string{evidence.RunPass}})
	if len(cases2) != 1 || cases2[0].Latest[e.ID].Status != evidence.RunPass {
		t.Fatalf("filter by latest on env: %+v", cases2)
	}
	sum, err := s.Summary(ctx, "ws", "", false)
	if err != nil || sum.Cases != 3 || sum.Recorded != 2 || sum.LastBatch == nil {
		t.Fatalf("summary: %+v %v", sum, err)
	}
	if bytes, _ := s.StorageBytes(ctx, "ws"); bytes != 1234 {
		t.Fatalf("storage: %d", bytes)
	}
	_, paths, err := s.DeleteCase(ctx, "ws", cases[0].Key)
	if err != nil || len(paths) != 1 || paths[0] != "/tmp/x.mp4" {
		t.Fatalf("delete: %v %v", paths, err)
	}
}

func TestCancelBatch(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	e := mustEnv(t, s, "ws", "Staging")
	recorded(t, s, "ws", 2)
	b, _ := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID})
	b, err := s.CancelBatch(ctx, "ws", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != evidence.BatchCanceled || b.Canceled != 2 || b.FinishedAt == nil {
		t.Fatalf("canceled: %+v", b)
	}
	if _, err := s.CancelBatch(ctx, "ws", b.ID); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("cancel twice: %v", err)
	}
	if ok, _ := s.StartBatch(ctx, b.ID, "pod"); ok {
		t.Fatal("a canceled batch started")
	}
	if _, err := s.DeleteEnvironment(ctx, "ws", e.ID); err != nil {
		t.Fatalf("delete env with only closed batches: %v", err)
	}
}

func TestDeleteEnvironmentRefusesWhileRunning(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	e := mustEnv(t, s, "ws", "Staging")
	recorded(t, s, "ws", 1)
	if _, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteEnvironment(ctx, "ws", e.ID); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("deleted under a queued batch: %v", err)
	}
}

func TestRecordings(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	e := mustEnv(t, s, "ws", "Staging")
	c, _ := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "x"})
	first, err := s.CreateRecording(ctx, "ws", c.ID, e.ID, "chat-1", "ana")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRecording(ctx, "ws", c.ID, e.ID, "chat-2", "ana"); err != nil {
		t.Fatal(err)
	}
	if s.IsRecordingChat(ctx, "chat-1") {
		t.Fatal("a newer recording of the same case must close the older one")
	}
	if !s.IsRecordingChat(ctx, "chat-2") {
		t.Fatal("chat-2 is recording")
	}
	if _, err := s.OpenRecordingForChat(ctx, "other", "chat-2"); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("other workspace found the recording: %v", err)
	}
	rec, err := s.OpenRecordingForChat(ctx, "ws", "chat-2")
	if err != nil || rec.ID == first.ID {
		t.Fatalf("open: %+v %v", rec, err)
	}
	if err := s.MarkRecordingSaved(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if s.IsRecordingChat(ctx, "chat-2") {
		t.Fatal("saved recording still open")
	}
}

type fakeLauncher struct {
	mu      sync.Mutex
	started []evidence.RunnerSpec
	exits   []func(string)
}

func (f *fakeLauncher) Launch(_ context.Context, spec evidence.RunnerSpec, onExit func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, spec)
	f.exits = append(f.exits, onExit)
	return "runner-" + spec.Batch.ID, nil
}

func (f *fakeLauncher) Stop(context.Context, string, string) {}

func (f *fakeLauncher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

type fakeKeys struct {
	mu      sync.Mutex
	revoked []string
}

func (k *fakeKeys) MintRunnerKey(_ context.Context, _, batch string, _ time.Duration) (string, string, error) {
	return "key-" + batch, "id-" + batch, nil
}

func (k *fakeKeys) RevokeAPIKey(_ context.Context, id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.revoked = append(k.revoked, id)
	return nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunnerSpeaksTheLanguageOfWhoQueued(t *testing.T) {
	prev := i18n.DaemonLocale()
	i18n.SetDaemonLocale("en")
	t.Cleanup(func() { i18n.SetDaemonLocale(prev) })
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := mustEnv(t, s, "ws", "Staging")
	recorded(t, s, "ws", 1)
	l := &fakeLauncher{}
	svc := &evidence.Service{Store: s, Launcher: l, Keys: &fakeKeys{}, MaxRunners: 2}
	svc.Start(ctx)
	asked, err := svc.Queue(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID, Locale: "es"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBatch(ctx, "ws", asked.ID); got.Locale != "es" {
		t.Fatalf("stored locale = %q, want es", got.Locale)
	}
	if _, err := svc.Queue(ctx, "ws", "schedule", evidence.BatchSpec{EnvironmentID: e.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both runners", func() bool { return l.count() == 2 })
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, spec := range l.started {
		want := "en"
		if spec.Batch.ID == asked.ID {
			want = "es"
		}
		if got := spec.Env["OCTOPUS_LOCALE"]; got != want {
			t.Errorf("batch %s: OCTOPUS_LOCALE = %q, want %q", spec.Batch.ID, got, want)
		}
	}
}

func TestServiceRespectsRunnerLimitAndClosesOnExit(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := mustEnv(t, s, "ws", "Staging")
	recorded(t, s, "ws", 1)
	l, k := &fakeLauncher{}, &fakeKeys{}
	svc := &evidence.Service{Store: s, Launcher: l, Keys: k, MaxRunners: 1, DaemonURL: func() string { return "http://daemon:8585" }}
	svc.Start(ctx)
	b1, err := svc.Queue(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := svc.Queue(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first runner", func() bool { return l.count() == 1 })
	l.mu.Lock()
	spec := l.started[0]
	l.mu.Unlock()
	if spec.Batch.ID != b1.ID || spec.Env["OCTOPUS_API_KEY"] != "key-"+b1.ID || spec.Env["EVIDENCE_BATCH_ID"] != b1.ID ||
		spec.Env["OCTOPUS_API"] != "http://daemon:8585" {
		t.Fatalf("runner spec: %+v", spec)
	}
	time.Sleep(100 * time.Millisecond)
	if l.count() != 1 {
		t.Fatal("second runner started past the limit")
	}
	got, _ := s.GetBatch(ctx, "ws", b1.ID)
	if got.Status != evidence.BatchRunning || got.Runner != "runner-"+b1.ID {
		t.Fatalf("running batch: %+v", got)
	}
	l.mu.Lock()
	exit := l.exits[0]
	l.mu.Unlock()
	exit("runner exited with code 1")
	waitFor(t, "second runner", func() bool { return l.count() == 2 })
	got, _ = s.GetBatch(ctx, "ws", b1.ID)
	if got.Status != evidence.BatchError || got.Errored != 1 {
		t.Fatalf("batch after crash: %+v", got)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.revoked) != 1 || k.revoked[0] != "id-"+b1.ID {
		t.Fatalf("key not revoked: %v", k.revoked)
	}
	_ = b2
}

func TestServiceClosesLeftoverBatchesAtStart(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := mustEnv(t, s, "ws", "Staging")
	recorded(t, s, "ws", 1)
	b, _ := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID})
	if ok, _ := s.StartBatch(ctx, b.ID, "old-pod"); !ok {
		t.Fatal("start")
	}
	(&evidence.Service{Store: s}).Start(ctx)
	got, _ := s.GetBatch(ctx, "ws", b.ID)
	if got.Status != evidence.BatchError || !strings.Contains(got.Error, "restarted") {
		t.Fatalf("leftover batch: %+v", got)
	}
}

func TestOnDeployFiresProjectEnvironmentsOfTheRepoAndWorkspace(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	db := s.DB()
	mustProject(t, db, "ws", "p-shop", "shop", "https://github.com/acme/shop.git")
	mustProject(t, db, "ws", "p-blog", "blog", "git@github.com:acme/blog.git")
	mustProject(t, db, "intruder", "p-spy", "spy", "https://github.com/acme/shop")
	prod := mustProjectEnv(t, s, ps, "ws", "p-shop", project.EnvironmentInput{Name: "production", URL: "https://shop.example.com"})
	stage := mustProjectEnv(t, s, ps, "ws", "p-shop", project.EnvironmentInput{Name: "stage", DisplayName: "QA Stage", URL: "https://qa.shop.example.com"})
	quiet := mustProjectEnv(t, s, ps, "ws", "p-shop", project.EnvironmentInput{Name: "preview", URL: "https://pre.shop.example.com"})
	blog := mustProjectEnv(t, s, ps, "ws", "p-blog", project.EnvironmentInput{Name: "production", URL: "https://blog.example.com"})
	spy := mustProjectEnv(t, s, ps, "intruder", "p-spy", project.EnvironmentInput{Name: "production", URL: "https://spy.example.com"})
	on := true
	for _, e := range []*evidence.Environment{prod, stage, blog, spy} {
		ws := e.WorkspaceID
		if _, err := s.UpdateEnvironment(ctx, ws, e.ID, evidence.EnvironmentPatch{OnDeploy: &on}, ps); err != nil {
			t.Fatal(err)
		}
	}
	if prod.Deploy == nil || !prod.Deploy.Available || prod.Deploy.Repo != "acme/shop" || prod.Deploy.Environment != "production" {
		t.Fatalf("deploy info: %+v", prod.Deploy)
	}
	recorded(t, s, "ws", 1)
	recorded(t, s, "intruder", 1)
	svc := &evidence.Service{Store: s}
	n := svc.OnDeploy(ctx, evidence.Deploy{Repo: "Acme/Shop", Environment: "Production", SHA: "0123456789abcdef", Workspaces: []string{"ws"}})
	if n != 1 {
		t.Fatalf("a production deploy fired %d environments", n)
	}
	bs, _ := s.ListBatches(ctx, "ws", prod.ID, "", 5)
	if len(bs) != 1 || bs[0].Trigger != evidence.TriggerDeploy || bs[0].TriggerRef != "Acme/Shop@0123456 · Production" ||
		bs[0].BaseURL != "https://shop.example.com" {
		t.Fatalf("deploy batch: %+v", bs)
	}
	if n := svc.OnDeploy(ctx, evidence.Deploy{Repo: "acme/shop", Environment: "qa stage", Workspaces: []string{"ws"}}); n != 1 {
		t.Fatalf("display name match fired %d", n)
	}
	if got := evidence.DeploySlug(" QA_Stage two "); got != "qa-stage-two" {
		t.Fatalf("slug: %q", got)
	}
	for _, e := range []*evidence.Environment{quiet, blog} {
		if bs, _ := s.ListBatches(ctx, "ws", e.ID, "", 5); len(bs) != 0 {
			t.Fatalf("%s fired: %+v", e.Name, bs)
		}
	}
	if bs, _ := s.ListBatches(ctx, "intruder", "", "", 5); len(bs) != 0 {
		t.Fatalf("intruder's environment fired: %+v", bs)
	}
}

func TestProjectRepoNotOnGitHubCannotDeploy(t *testing.T) {
	s, ps := newStores(t)
	db := s.DB()
	mustProject(t, db, "ws", "p1", "local", "file:///srv/git/local.git")
	e := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "staging", URL: "https://s.example.com"})
	if e.Deploy == nil || e.Deploy.Available || e.Deploy.Reason != evidence.ReasonRepoNotGitHub {
		t.Fatalf("deploy info: %+v", e.Deploy)
	}
}

func TestSchedulesFireOnceWhenDue(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	e, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "Nightly", BaseURL: "https://a.b", Schedule: "0 3 * * *"})
	if err != nil {
		t.Fatal(err)
	}
	recorded(t, s, "ws", 1)
	svc := &evidence.Service{Store: s, Schedule: func(string, string) (func(time.Time) time.Time, error) {
		return func(from time.Time) time.Time { return from.Add(time.Hour) }, nil
	}}
	svc.FireSchedules(ctx, e.CreatedAt.Add(30*time.Minute))
	if bs, _ := s.ListBatches(ctx, "ws", e.ID, "", 5); len(bs) != 0 {
		t.Fatalf("fired early: %+v", bs)
	}
	later := e.CreatedAt.Add(5 * time.Hour)
	svc.FireSchedules(ctx, later)
	svc.FireSchedules(ctx, later.Add(time.Minute))
	bs, _ := s.ListBatches(ctx, "ws", e.ID, "", 5)
	if len(bs) != 1 || bs[0].Trigger != evidence.TriggerSchedule {
		t.Fatalf("a daemon that slept fires once: %+v", bs)
	}
}

func TestScheduleScopeNarrowsTheBatch(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "https://github.com/acme/shop.git")
	mustProject(t, s.DB(), "other", "p9", "theirs", "https://github.com/acme/theirs.git")
	mk := func(project string, tags ...string) {
		if _, err := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "c", ProjectID: project, Tags: tags,
			Take: mustTake(t), TakeSource: evidence.SourceUpload}); err != nil {
			t.Fatal(err)
		}
	}
	mk("p1", "checkout")
	mk("p1")
	mk("", "checkout")
	if _, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "Bad", BaseURL: "https://a.b",
		ScheduleProjectID: "p9"}); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("another workspace's project: %v", err)
	}
	e, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "Site", BaseURL: "https://a.b",
		Schedule: "0 3 * * *", ScheduleProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if e.ScheduleProjectID != "p1" {
		t.Fatalf("scope not kept: %+v", e)
	}
	svc := &evidence.Service{Store: s, Schedule: func(string, string) (func(time.Time) time.Time, error) {
		return func(from time.Time) time.Time { return from.Add(time.Hour) }, nil
	}}
	svc.FireSchedules(ctx, e.CreatedAt.Add(2*time.Hour))
	bs, _ := s.ListBatches(ctx, "ws", e.ID, "", 5)
	if len(bs) != 1 || bs[0].Total != 2 {
		t.Fatalf("project scope: %+v", bs)
	}
	tag := "checkout"
	if e, err = s.UpdateEnvironment(ctx, "ws", e.ID, evidence.EnvironmentPatch{ScheduleTag: &tag}, nil); err != nil {
		t.Fatal(err)
	}
	svc.FireSchedules(ctx, e.CreatedAt.Add(4*time.Hour))
	bs, _ = s.ListBatches(ctx, "ws", e.ID, "", 5)
	if len(bs) != 2 || bs[0].Total != 1 {
		t.Fatalf("project and tag scope: %+v", bs)
	}
}

func TestProjectRowsReadTheirProjectEnvironment(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "https://github.com/acme/shop.git")
	e := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "staging", DisplayName: "Staging",
		URL: "https://staging.shop.example.com/", IsProduction: false})
	if e.Kind != evidence.KindProject || e.ProjectName != "shop" || e.Name != "staging" || e.Label() != "Staging" ||
		e.BaseURL != "https://staging.shop.example.com" {
		t.Fatalf("project row: %+v", e)
	}
	sb := sandboxEnv(t, s, "ws", "p1")
	if sb.Kind != evidence.KindSandbox || sb.Name != evidence.SandboxName || sb.BaseURL != "" || sb.DefaultBranch != "main" {
		t.Fatalf("sandbox row: %+v", sb)
	}
	if err := s.EnsureProjectRows(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	envs, err := s.ListEnvironments(ctx, "ws", "")
	if err != nil || len(envs) != 2 || envs[0].Kind != evidence.KindProject || envs[1].Kind != evidence.KindSandbox {
		t.Fatalf("list: %+v %v", envs, err)
	}

	name, url := "Staging EU", "https://eu.shop.example.com"
	e, err = s.UpdateEnvironment(ctx, "ws", e.ID, evidence.EnvironmentPatch{DisplayName: &name, BaseURL: &url}, ps)
	if err != nil {
		t.Fatal(err)
	}
	pe, _ := ps.GetEnvironment(ctx, "p1", e.ProjectEnvironmentID)
	if e.Label() != name || e.BaseURL != url || pe.DisplayName != name || pe.URL != url || pe.Provider != "other" {
		t.Fatalf("after patch: %+v / %+v", e, pe)
	}
	slug := "other"
	if _, err := s.UpdateEnvironment(ctx, "ws", e.ID, evidence.EnvironmentPatch{Name: &slug}, ps); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("name of a project row changed: %v", err)
	}
	prod := true
	if _, err := s.UpdateEnvironment(ctx, "ws", sb.ID, evidence.EnvironmentPatch{IsProduction: &prod}, ps); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("production of a sandbox changed: %v", err)
	}
	if _, err := s.DeleteEnvironment(ctx, "ws", e.ID); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("deleted a project row: %v", err)
	}
	if _, err := s.DeleteEnvironment(ctx, "ws", sb.ID); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("deleted a sandbox row: %v", err)
	}
}

func TestCreateProjectEnvironment(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "https://github.com/acme/shop.git")
	mustProject(t, s.DB(), "other", "p2", "theirs", "")
	e, err := s.CreateProjectEnvironment(ctx, "ws", "ana", ps, evidence.ProjectEnvironmentInput{ProjectID: "p1",
		Name: "qa", DisplayName: "QA", BaseURL: "https://qa.example.com/", AllowedHosts: []string{"auth.example.com"},
		Schedule: "0 7 * * *", OnDeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != evidence.KindProject || e.Label() != "QA" || e.BaseURL != "https://qa.example.com" || !e.OnDeploy ||
		e.Schedule != "0 7 * * *" || len(e.AllowedHosts) != 1 || e.CreatedBy != "ana" {
		t.Fatalf("created: %+v", e)
	}
	if _, err := s.CreateProjectEnvironment(ctx, "ws", "ana", ps, evidence.ProjectEnvironmentInput{ProjectID: "p1",
		Name: "qa", BaseURL: "https://x.example.com"}); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("duplicate slug: %v", err)
	}
	if _, err := s.CreateProjectEnvironment(ctx, "ws", "ana", ps, evidence.ProjectEnvironmentInput{ProjectID: "p2",
		Name: "qa", BaseURL: "https://x.example.com"}); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("another workspace's project: %v", err)
	}
	if _, err := s.CreateProjectEnvironment(ctx, "ws", "ana", ps, evidence.ProjectEnvironmentInput{ProjectID: "p1",
		Name: "Bad Slug!", BaseURL: "https://x.example.com"}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("invalid slug: %v", err)
	}
	envs, _ := ps.ListEnvironments(ctx, "p1")
	if len(envs) != 1 {
		t.Fatalf("a refused create left a project environment: %+v", envs)
	}
}

func TestCasesRunOnlyWhereTheirProjectIs(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	mustProject(t, s.DB(), "ws", "p2", "blog", "")
	shopEnv := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "staging", URL: "https://s.example.com"})
	site := mustEnv(t, s, "ws", "Site")
	shopCase, _ := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "Shop", ProjectID: "p1", Take: mustTake(t), TakeSource: evidence.SourceUpload})
	blogCase, _ := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "Blog", ProjectID: "p2", Take: mustTake(t), TakeSource: evidence.SourceUpload})
	anyCase, _ := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "Any", Take: mustTake(t), TakeSource: evidence.SourceUpload})

	b, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: shopEnv.ID})
	if err != nil || b.Total != 2 || b.BaseURL != "https://s.example.com" {
		t.Fatalf("project env batch: %+v %v", b, err)
	}
	keys := map[string]bool{}
	for _, r := range b.Runs {
		keys[r.CaseKey] = true
	}
	if !keys[shopCase.Key] || !keys[anyCase.Key] || keys[blogCase.Key] {
		t.Fatalf("project env ran %v", keys)
	}
	_, err = s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: shopEnv.ID, CaseIDs: []string{blogCase.Key, shopCase.Key}})
	if apperr.KindOf(err) != apperr.KindConflict || !strings.Contains(apperr.Localize(err, "en"), blogCase.Key) {
		t.Fatalf("a blog case on the shop env: %v", err)
	}
	sb := sandboxEnv(t, s, "ws", "p1")
	b, err = s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: sb.ID})
	if err != nil || b.Total != 1 || b.Runs[0].CaseKey != shopCase.Key || b.SandboxStatus != evidence.SandboxPending ||
		b.SourceBranch != "main" || b.BaseURL != "" {
		t.Fatalf("sandbox batch: %+v %v", b, err)
	}
	if _, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: sb.ID, CaseIDs: []string{anyCase.Key}}); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("a project-less case on a sandbox: %v", err)
	}
	b, err = s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: site.ID})
	if err != nil || b.Total != 3 {
		t.Fatalf("standalone runs everything: %+v %v", b, err)
	}
	if !evidence.CaseRunsOn("", *shopEnv) || evidence.CaseRunsOn("p2", *shopEnv) || evidence.CaseRunsOn("", *sb) || !evidence.CaseRunsOn("p2", *site) {
		t.Fatal("CaseRunsOn disagrees with CreateBatch")
	}
	b, err = s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: site.ID, ProjectID: "p1", IncludeUnassigned: true})
	if err != nil || b.Total != 2 {
		t.Fatalf("p1 with unassigned on the site: %+v %v", b, err)
	}
	cases, _, _ := s.ListCases(ctx, evidence.CaseFilter{WorkspaceID: "ws", ProjectID: "p1", IncludeUnassigned: true})
	if len(cases) != 2 {
		t.Fatalf("p1 with unassigned: %d cases", len(cases))
	}
	cases, _, _ = s.ListCases(ctx, evidence.CaseFilter{WorkspaceID: "ws", ProjectID: evidence.ProjectNone})
	if len(cases) != 1 || cases[0].Key != anyCase.Key {
		t.Fatalf("no project: %+v", cases)
	}
	sum, err := s.Summary(ctx, "ws", "p2", false)
	if err != nil || sum.Cases != 1 {
		t.Fatalf("summary of p2: %+v %v", sum, err)
	}
}

func TestLibraryFilters(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	mustProject(t, s.DB(), "ws", "p2", "blog", "")
	staging := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "staging", URL: "https://s.example.com"})
	mk := func(title, projectID, priority string, tags ...string) *evidence.Case {
		c, err := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: title, ProjectID: projectID, Priority: priority,
			Tags: tags, Take: mustTake(t), TakeSource: evidence.SourceUpload})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	pay := mk("Pay", "p1", evidence.PriorityCritical, "checkout", "smoke")
	cart := mk("Cart", "p1", evidence.PriorityHigh, "checkout")
	mk("Login", "", evidence.PriorityHigh, "auth", "smoke")
	mk("Post", "p2", evidence.PriorityLow, "blog")

	keys := func(cases []evidence.Case) string {
		var out []string
		for i := range cases {
			out = append(out, cases[i].Title)
		}
		return strings.Join(out, ",")
	}
	list := func(f evidence.CaseFilter) ([]evidence.Case, map[string]int) {
		t.Helper()
		f.WorkspaceID = "ws"
		cases, counts, err := s.ListCases(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return cases, counts
	}
	if got, _ := list(evidence.CaseFilter{Tags: []string{"checkout", "smoke"}}); keys(got) != "Pay" {
		t.Fatalf("every tag: %s", keys(got))
	}
	if got, _ := list(evidence.CaseFilter{Priorities: []string{evidence.PriorityHigh, evidence.PriorityCritical}}); keys(got) != "Login,Cart,Pay" {
		t.Fatalf("priorities: %s", keys(got))
	}

	b, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: staging.ID, CaseIDs: []string{pay.ID, cart.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, startErr := s.StartBatch(ctx, b.ID, "pod-1"); !ok || startErr != nil {
		t.Fatalf("start: %v %v", ok, startErr)
	}
	for range 2 {
		run, claimErr := s.ClaimNextRun(ctx, "ws", b.ID)
		if claimErr != nil || run == nil {
			t.Fatalf("claim: %v %v", run, claimErr)
		}
		st := evidence.RunPass
		if run.CaseID == cart.ID {
			st = evidence.RunFail
		}
		if _, finishErr := s.FinishRun(ctx, "ws", b.ID, run.ID, evidence.RunResult{Status: st}); finishErr != nil {
			t.Fatal(finishErr)
		}
	}
	got, counts := list(evidence.CaseFilter{EnvironmentID: staging.ID})
	if keys(got) != "Login,Cart,Pay" || counts[evidence.RunPass] != 1 || counts[evidence.RunFail] != 1 || counts[evidence.NotRunHere] != 1 {
		t.Fatalf("on staging: %s %v", keys(got), counts)
	}
	if got, _ = list(evidence.CaseFilter{EnvironmentID: staging.ID, Statuses: []string{evidence.RunFail}}); keys(got) != "Cart" {
		t.Fatalf("fails on staging: %s", keys(got))
	}
	if got, _ = list(evidence.CaseFilter{EnvironmentID: staging.ID, Statuses: []string{evidence.NotRunHere}}); keys(got) != "Login" {
		t.Fatalf("not run on staging: %s", keys(got))
	}
	if _, _, err = s.ListCases(ctx, evidence.CaseFilter{WorkspaceID: "other", EnvironmentID: staging.ID}); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("another workspace's environment: %v", err)
	}

	tags, err := s.CaseTags(ctx, "ws", "p1", true)
	if err != nil || strings.Join(tags, ",") != "auth,checkout,smoke" {
		t.Fatalf("tags of shop with unassigned: %v %v", tags, err)
	}
	if tags, _ = s.CaseTags(ctx, "ws", evidence.ProjectNone, false); strings.Join(tags, ",") != "auth,smoke" {
		t.Fatalf("tags of no project: %v", tags)
	}
}

func TestBatchURLAndBranchRules(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	noURL := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "local"})
	recorded(t, s, "ws", 1)
	if _, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: noURL.ID}); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Fatalf("no URL: %v", err)
	}
	site := mustEnv(t, s, "ws", "Site")
	if _, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: site.ID, Branch: "main"}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("branch on a site: %v", err)
	}
	if _, err := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "Shop", ProjectID: "p1", Take: mustTake(t), TakeSource: evidence.SourceUpload}); err != nil {
		t.Fatal(err)
	}
	sb := sandboxEnv(t, s, "ws", "p1")
	if _, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: sb.ID, Branch: "bad..branch"}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("bad branch: %v", err)
	}
	b, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: sb.ID, Branch: "feat/login"})
	if err != nil || b.SourceBranch != "feat/login" {
		t.Fatalf("branch batch: %+v %v", b, err)
	}
	url := "https://moved.example.com"
	if _, err := s.UpdateEnvironment(ctx, "ws", site.ID, evidence.EnvironmentPatch{BaseURL: &url}, ps); err != nil {
		t.Fatal(err)
	}
	b2, _ := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: site.ID})
	if b2.BaseURL != url {
		t.Fatalf("new batch URL: %s", b2.BaseURL)
	}
}

func TestFailSandboxBatchLeavesCasesAlone(t *testing.T) {
	s, _ := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	c, _ := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "Shop", ProjectID: "p1", Take: mustTake(t), TakeSource: evidence.SourceUpload})
	sb := sandboxEnv(t, s, "ws", "p1")
	b, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: sb.ID})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.StartBatch(ctx, b.ID, ""); !ok {
		t.Fatal("start")
	}
	if err := s.MarkSandboxStarting(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSandboxPhase(ctx, b.ID, evidence.PhaseSetup); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetBatch(ctx, "ws", b.ID)
	if got.SandboxStatus != evidence.SandboxStarting || got.SandboxPhase != evidence.PhaseSetup {
		t.Fatalf("starting: %+v", got)
	}
	log := strings.Repeat("é", 2000) + "npm ERR! missing script: start"
	reason := evidence.SandboxReason(evidence.SandboxStartFailed, "code", 1)
	if err := s.FailSandboxBatch(ctx, b.ID, reason, log); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseBatch(ctx, b.ID, "runner exited"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetBatch(ctx, "ws", b.ID)
	if got.Status != evidence.BatchError || got.SandboxStatus != evidence.SandboxFailed || got.SandboxError != reason.String() ||
		!strings.Contains(got.Error, reason.String()) || got.Canceled != 1 || got.FinishedAt == nil {
		t.Fatalf("failed batch: %+v", got)
	}
	if len(got.SandboxLog) > 3000 || !strings.HasSuffix(got.SandboxLog, "missing script: start") || !utf8.ValidString(got.SandboxLog) {
		t.Fatalf("log tail: %d bytes, valid %v", len(got.SandboxLog), utf8.ValidString(got.SandboxLog))
	}
	if got.Runs[0].Status != evidence.RunCanceled || got.Runs[0].Error != reason.String() {
		t.Fatalf("run: %+v", got.Runs[0])
	}
	es, _ := s.GetBatch(i18n.WithLocale(ctx, "es"), "ws", b.ID)
	if want := reason.Text("es"); es.SandboxError != want || !strings.Contains(es.Error, want) ||
		!strings.HasPrefix(es.Error, "No se pudo") || es.Runs[0].Error != want || es.SandboxReason != evidence.SandboxStartFailed {
		t.Fatalf("in Spanish: %q / %q / %q", es.SandboxError, es.Error, es.Runs[0].Error)
	}
	if c2, _ := s.GetCase(ctx, "ws", c.ID, false); c2.Status != c.Status {
		t.Fatalf("case moved from %s to %s", c.Status, c2.Status)
	}
	if ok, _ := s.MarkSandboxReady(ctx, b.ID, "http://x", "abc"); ok {
		t.Fatal("a failed batch became ready")
	}
	list, _ := s.ListBatches(ctx, "ws", sb.ID, "", 5)
	if len(list) != 1 || list[0].SandboxLog != "" {
		t.Fatalf("the list carries the log: %+v", list)
	}
}

func TestSandboxReadyFreezesURL(t *testing.T) {
	s, _ := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	if _, err := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "Shop", ProjectID: "p1", Take: mustTake(t), TakeSource: evidence.SourceUpload}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: sandboxEnv(t, s, "ws", "p1").ID})
	if ok, _ := s.StartBatch(ctx, b.ID, ""); !ok {
		t.Fatal("start")
	}
	if err := s.MarkSandboxStarting(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	ok, err := s.MarkSandboxReady(ctx, b.ID, "http://sandbox-evidence-x:3000/", "abc123")
	if err != nil || !ok {
		t.Fatalf("ready: %v %v", ok, err)
	}
	h, _ := s.BatchHeader(ctx, "ws", b.ID)
	if h.BaseURL != "http://sandbox-evidence-x:3000" || h.SourceCommit != "abc123" || h.SandboxStatus != evidence.SandboxReady {
		t.Fatalf("ready batch: %+v", h)
	}
	if err := s.MarkSandboxReleased(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if h, _ = s.BatchHeader(ctx, "ws", b.ID); h.SandboxStatus != evidence.SandboxReleased {
		t.Fatalf("released: %s", h.SandboxStatus)
	}
}

func TestDetachKeepsRunsWhenTheProjectEnvironmentGoes(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	e := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "staging", DisplayName: "Staging",
		URL: "https://s.example.com", IsProduction: true})
	if _, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "Staging (shop)", BaseURL: "https://x.example.com"}); err != nil {
		t.Fatal(err)
	}
	recorded(t, s, "ws", 1)
	b, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DetachProjectEnvironment(ctx, e.ProjectEnvironmentID); err != nil {
		t.Fatal(err)
	}
	if err := ps.DeleteEnvironment(ctx, "p1", e.ProjectEnvironmentID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEnvironment(ctx, "ws", e.ID)
	if err != nil || got.Kind != evidence.KindStandalone || got.Name != "Staging (shop) 2" || got.BaseURL != "https://s.example.com" ||
		!got.IsProduction || got.ProjectID != "" || got.OnDeploy {
		t.Fatalf("detached: %+v %v", got, err)
	}
	if bs, _ := s.ListBatches(ctx, "ws", e.ID, "", 5); len(bs) != 1 || bs[0].ID != b.ID {
		t.Fatalf("runs lost: %+v", bs)
	}
	if err := s.EnsureProjectRows(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	if envs, _ := s.ListEnvironments(ctx, "ws", "p1"); len(envs) != 1 || envs[0].Kind != evidence.KindSandbox {
		t.Fatalf("p1 now has %+v", envs)
	}
}

func TestDetachProject(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	e := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "staging", URL: "https://s.example.com"})
	c, _ := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "Shop", ProjectID: "p1", Take: mustTake(t), TakeSource: evidence.SourceUpload})
	sb := sandboxEnv(t, s, "ws", "p1")
	sbBatch, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: sb.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddArtifact(ctx, evidence.Artifact{WorkspaceID: "ws", RunID: sbBatch.Runs[0].ID, Kind: evidence.ArtifactVideo,
		Filename: "v.mp4", Path: "/tmp/sandbox-v.mp4"}); err != nil {
		t.Fatal(err)
	}
	paths, err := s.DetachProject(ctx, "p1")
	if err != nil || len(paths) != 1 || paths[0] != "/tmp/sandbox-v.mp4" {
		t.Fatalf("detach project: %v %v", paths, err)
	}
	if got, _ := s.GetEnvironment(ctx, "ws", e.ID); got == nil || got.Kind != evidence.KindStandalone || got.Name != "staging (shop)" {
		t.Fatalf("environment after: %+v", got)
	}
	if _, err := s.GetEnvironment(ctx, "ws", sb.ID); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("sandbox survived: %v", err)
	}
	if c2, _ := s.GetCase(ctx, "ws", c.ID, false); c2.ProjectID != "" {
		t.Fatalf("case kept its project: %+v", c2)
	}
}

func TestEnsureHealsRowsOfADeletedProject(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "staging", URL: "https://s.example.com"})
	if _, err := s.DetachProject(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureProjectRows(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	envs, err := s.ListEnvironments(ctx, "ws", "p1")
	if err != nil || len(envs) != 2 {
		t.Fatalf("recreated rows: %+v %v", envs, err)
	}
	var withRuns, sb string
	for i := range envs {
		if envs[i].Kind == evidence.KindProject {
			withRuns = envs[i].ID
		} else {
			sb = envs[i].ID
		}
	}
	recorded(t, s, "ws", 1)
	if _, err = s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: withRuns}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`DELETE FROM project_environments WHERE project_id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`DELETE FROM projects WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureProjectRows(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetEnvironment(ctx, "ws", withRuns); got == nil || got.Kind != evidence.KindStandalone || got.ProjectID != "" {
		t.Fatalf("row with runs: %+v", got)
	}
	if _, err = s.GetEnvironment(ctx, "ws", sb); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("empty Sandbox row survived: %v", err)
	}
}

func TestLinkEnvironment(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	site := mustEnv(t, s, "ws", "Old staging")
	pw := "x"
	if _, err := s.SetSecrets(ctx, "ws", site.ID, map[string]*string{"QA_PASSWORD": &pw}); err != nil {
		t.Fatal(err)
	}
	recorded(t, s, "ws", 1)
	if _, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: site.ID}); err != nil {
		t.Fatal(err)
	}
	target := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "staging", URL: "https://s.example.com"})
	linked, err := s.LinkEnvironment(ctx, "ws", site.ID, target.ProjectEnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.ID != site.ID || linked.Kind != evidence.KindProject || linked.ProjectID != "p1" ||
		linked.BaseURL != "https://s.example.com" || len(linked.SecretNames) != 1 {
		t.Fatalf("linked: %+v", linked)
	}
	if _, err := s.GetEnvironment(ctx, "ws", target.ID); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("the empty row stayed: %v", err)
	}
	if _, err := s.LinkEnvironment(ctx, "ws", site.ID, target.ProjectEnvironmentID); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("linked a project row: %v", err)
	}
	busy := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "qa", URL: "https://qa.example.com"})
	if _, err := s.CreateBatch(ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: busy.ID}); err != nil {
		t.Fatal(err)
	}
	other := mustEnv(t, s, "ws", "Other")
	if _, err := s.LinkEnvironment(ctx, "ws", other.ID, busy.ProjectEnvironmentID); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("replaced a row with runs: %v", err)
	}
	if _, err := s.LinkEnvironment(ctx, "intruder", other.ID, busy.ProjectEnvironmentID); apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("another workspace linked: %v", err)
	}
}

func TestScheduledEnvironmentsSkipSandboxAndEmptyURL(t *testing.T) {
	s, ps := newStores(t)
	ctx := context.Background()
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	empty := mustProjectEnv(t, s, ps, "ws", "p1", project.EnvironmentInput{Name: "local"})
	cron := "0 3 * * *"
	if _, err := s.UpdateEnvironment(ctx, "ws", empty.ID, evidence.EnvironmentPatch{Schedule: &cron}, ps); err != nil {
		t.Fatal(err)
	}
	full, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "Nightly", BaseURL: "https://a.b", Schedule: cron})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := s.ScheduledEnvironments(ctx)
	if err != nil || len(envs) != 1 || envs[0].ID != full.ID {
		t.Fatalf("scheduled: %+v %v", envs, err)
	}
}

func TestMigration2KeepsMigration1Data(t *testing.T) {
	db := testpg.New(t)
	ctx := context.Background()
	ps, err := project.NewStore(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all, err := evidence.Migrations(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, db, evidence.MigrationComponent, all[:1]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO evidence_environments (id, workspace_id, name, base_url, deploy_repo, created_at, updated_at)
			VALUES ('env1', 'ws', 'Sitio de ejemplo', 'https://example.com', 'acme/web', NOW(), NOW())`,
		`INSERT INTO evidence_cases (id, workspace_id, key, title, take, status, created_at, updated_at)
			VALUES ('c1', 'ws', 'EV-1', 'Portada', '{"version":1}', 'pass', NOW(), NOW()),
			       ('c2', 'ws', 'EV-2', 'Contacto', '{"version":1}', 'fail', NOW(), NOW())`,
		`INSERT INTO evidence_batches (id, workspace_id, environment_id, trigger, status, total, passed, failed, created_at)
			VALUES ('b1', 'ws', 'env1', 'manual', 'done', 2, 1, 1, NOW())`,
		`INSERT INTO evidence_runs (id, workspace_id, batch_id, case_id, environment_id, status, created_at)
			VALUES ('r1', 'ws', 'b1', 'c1', 'env1', 'pass', NOW()), ('r2', 'ws', 'b1', 'c2', 'env1', 'fail', NOW())`,
		`INSERT INTO evidence_artifacts (id, workspace_id, run_id, kind, filename, path, created_at)
			VALUES ('a1', 'ws', 'r1', 'video', 'v.mp4', '/data/evidence/ws/r1/v.mp4', NOW())`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mustProject(t, db, "ws", "p1", "web", "https://github.com/acme/web")
	if _, err := ps.CreateEnvironment(ctx, "p1", project.EnvironmentInput{Name: "production", URL: "https://web.example.com"}); err != nil {
		t.Fatal(err)
	}

	s, err := evidence.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	site, err := s.GetEnvironment(ctx, "ws", "env1")
	if err != nil || site.Kind != evidence.KindStandalone || site.Name != "Sitio de ejemplo" || site.BaseURL != "https://example.com" ||
		site.OnDeploy || site.IsProduction {
		t.Fatalf("standalone site: %+v %v", site, err)
	}
	b, err := s.GetBatch(ctx, "ws", "b1")
	if err != nil || len(b.Runs) != 2 || b.BaseURL != "" || b.SandboxStatus != "" || b.Passed != 1 {
		t.Fatalf("old batch: %+v %v", b, err)
	}
	if a, err := s.GetArtifact(ctx, "ws", "a1"); err != nil || a.RunID != "r1" {
		t.Fatalf("artifact: %+v %v", a, err)
	}
	envs, err := s.ListEnvironments(ctx, "ws", "")
	if err != nil || len(envs) != 3 {
		t.Fatalf("after migration: %+v %v", envs, err)
	}
	kinds := map[string]int{}
	for i := range envs {
		kinds[envs[i].Kind]++
	}
	if kinds[evidence.KindProject] != 1 || kinds[evidence.KindSandbox] != 1 || kinds[evidence.KindStandalone] != 1 {
		t.Fatalf("kinds: %v", kinds)
	}
	if _, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "production", BaseURL: "https://p.example.com"}); err != nil {
		t.Fatalf("standalone named like a project env: %v", err)
	}
	if _, err := s.CreateEnvironment(ctx, "ws", "ana", evidence.Environment{Name: "Sitio de ejemplo", BaseURL: "https://e.example.com"}); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("standalone names stay unique: %v", err)
	}
}
