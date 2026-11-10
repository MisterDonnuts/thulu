package evidence

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Job routes authorize a runner key by its agent, RunnerAgentPrefix plus the batch id.
const (
	RunnerStage       = "evidence"
	RunnerAgentPrefix = "evidence:"
)

type RunnerSpec struct {
	Batch    Batch
	Env      map[string]string
	Deadline time.Duration
	// On Kubernetes the runner pod carries it as its sandbox root, which the sandbox's preview
	// policies admit.
	SandboxOwner string
}

// onExit must be called exactly once, from any goroutine, when the runner is gone (exit, crash,
// deleted).
type Launcher interface {
	Launch(ctx context.Context, spec RunnerSpec, onExit func(reason string)) (string, error)
	Stop(ctx context.Context, workspaceID, name string)
}

type Keys interface {
	MintRunnerKey(ctx context.Context, workspaceID, batchID string, ttl time.Duration) (key, keyID string, err error)
	RevokeAPIKey(ctx context.Context, keyID string) error
}

type ScheduleFunc func(expr, tz string) (func(time.Time) time.Time, error)

type Service struct {
	Store         *Store
	Launcher      Launcher
	Keys          Keys
	DaemonURL     func() string
	Schedule      ScheduleFunc
	MaxRunners    int
	BatchDeadline time.Duration
	Sandbox       SandboxStarter

	mu      sync.Mutex
	running map[string]string
	kick    chan struct{}
	starts  map[string]context.CancelFunc
}

const (
	DefaultMaxRunners    = 2
	DefaultBatchDeadline = 2 * time.Hour
	tickEvery            = time.Minute
)

func (s *Service) maxRunners() int {
	if s.MaxRunners > 0 {
		return s.MaxRunners
	}
	return DefaultMaxRunners
}

func (s *Service) deadline() time.Duration {
	if s.BatchDeadline > 0 {
		return s.BatchDeadline
	}
	return DefaultBatchDeadline
}

func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.running = map[string]string{}
	s.kick = make(chan struct{}, 1)
	s.starts = map[string]context.CancelFunc{}
	s.mu.Unlock()
	logx.Warn(s.Store.EnsureProjectRows(ctx, ""), "evidence.log.ensure_rows_failed")
	if bs, err := s.Store.RunningBatches(ctx); err == nil {
		s.reapSandboxes(ctx, bs)
		for i := range bs {
			b := &bs[i]
			if s.Launcher != nil && b.Runner != "" {
				s.Launcher.Stop(ctx, b.WorkspaceID, b.Runner)
			}
			reason := i18n.T(i18n.SourceLocale, "evidence.reasons.daemon_restarted")
			logx.Warn(s.Store.CloseBatch(ctx, b.ID, reason), "evidence.log.close_failed", "batch", b.ID)
		}
	} else {
		logx.Warn(err, "evidence.log.list_failed")
	}
	go s.loop(ctx)
	s.Kick()
}

func (s *Service) Kick() {
	s.mu.Lock()
	k := s.kick
	s.mu.Unlock()
	if k == nil {
		return
	}
	select {
	case k <- struct{}{}:
	default:
	}
}

func (s *Service) loop(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			logx.Warn(s.Store.EnsureProjectRows(ctx, ""), "evidence.log.ensure_rows_failed")
			s.FireSchedules(ctx, time.Now())
			s.startQueued(ctx)
		case <-s.kick:
			s.startQueued(ctx)
		}
	}
}

func (s *Service) startQueued(ctx context.Context) {
	if s.Launcher == nil || s.Keys == nil {
		return
	}
	queued, err := s.Store.QueuedBatches(ctx)
	if err != nil {
		logx.Warn(err, "evidence.log.list_failed")
		return
	}
	for i := range queued {
		b := &queued[i]
		if !s.reserve(b) {
			continue
		}
		if err := s.launch(ctx, b); err != nil {
			s.release(b.ID)
			logx.Warn(err, "evidence.log.launch_failed", "batch", b.ID)
			logx.Warn(s.Store.CloseBatch(ctx, b.ID, apperr.Localize(err, i18n.SourceLocale)), "evidence.log.close_failed", "batch", b.ID)
		}
	}
}

func (s *Service) reserve(b *Batch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ws := range s.running {
		if ws == b.WorkspaceID {
			n++
		}
	}
	if n >= s.maxRunners() {
		return false
	}
	s.running[b.ID] = b.WorkspaceID
	return true
}

func (s *Service) release(batchID string) {
	s.mu.Lock()
	delete(s.running, batchID)
	s.mu.Unlock()
}

func (s *Service) launch(ctx context.Context, b *Batch) error {
	ok, err := s.Store.StartBatch(ctx, b.ID, "")
	if err != nil {
		return err
	}
	if !ok {
		s.release(b.ID)
		return nil
	}
	if b.SandboxStatus != "" {
		return s.launchSandbox(ctx, b)
	}
	return s.runRunner(ctx, b, "", nil)
}

func (s *Service) runRunner(ctx context.Context, b *Batch, sandboxOwner string, afterExit func()) error {
	key, keyID, err := s.Keys.MintRunnerKey(ctx, b.WorkspaceID, b.ID, s.deadline()+10*time.Minute)
	if err != nil {
		return err
	}
	url := ""
	if s.DaemonURL != nil {
		url = s.DaemonURL()
	}
	spec := RunnerSpec{Batch: *b, Deadline: s.deadline(), SandboxOwner: sandboxOwner, Env: map[string]string{
		"OCTOPUS_API":       url,
		"OCTOPUS_API_KEY":   key,
		"EVIDENCE_BATCH_ID": b.ID,
		"OCTOPUS_LOCALE":    b.runLocale(),
	}}
	var once sync.Once
	onExit := func(reason string) {
		once.Do(func() {
			bg := context.Background()
			logx.Warn(s.Keys.RevokeAPIKey(bg, keyID), "evidence.log.revoke_failed", "batch", b.ID)
			logx.Warn(s.Store.CloseBatch(bg, b.ID, reason), "evidence.log.close_failed", "batch", b.ID)
			if afterExit != nil {
				afterExit()
			}
			s.release(b.ID)
			s.Kick()
		})
	}
	name, err := s.Launcher.Launch(ctx, spec, onExit)
	if err != nil {
		logx.Warn(s.Keys.RevokeAPIKey(ctx, keyID), "evidence.log.revoke_failed", "batch", b.ID)
		return err
	}
	_, err = s.Store.db.ExecContext(ctx, s.Store.rb(`UPDATE evidence_batches SET runner = ? WHERE id = ?`), name, b.ID)
	return err
}

func (s *Service) Queue(ctx context.Context, ws, author string, spec BatchSpec) (*Batch, error) {
	b, err := s.Store.CreateBatch(ctx, ws, author, spec)
	if err != nil {
		return nil, err
	}
	s.Kick()
	return b, nil
}

type SandboxBatchSpec struct {
	ProjectID  string
	Branch     string
	CaseIDs    []string
	Tag        string
	Trigger    string
	TriggerRef string
	Locale     string
}

func (s *Service) QueueSandbox(ctx context.Context, ws, author string, spec SandboxBatchSpec) (*Batch, error) {
	env, err := s.Store.SandboxEnvironment(ctx, ws, spec.ProjectID)
	if err != nil {
		return nil, err
	}
	return s.Queue(ctx, ws, author, BatchSpec{EnvironmentID: env.ID, CaseIDs: spec.CaseIDs, Tag: spec.Tag,
		Branch: spec.Branch, Trigger: spec.Trigger, TriggerRef: spec.TriggerRef, Locale: spec.Locale})
}

func (s *Service) CancelBatch(ctx context.Context, ws, id string) (*Batch, error) {
	b, err := s.Store.CancelBatch(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	s.cancelSandboxStart(id)
	return b, nil
}

func (s *Service) trackSandboxStart(batchID string, cancel context.CancelFunc) {
	s.mu.Lock()
	if s.starts == nil {
		s.starts = map[string]context.CancelFunc{}
	}
	s.starts[batchID] = cancel
	s.mu.Unlock()
}

func (s *Service) untrackSandboxStart(batchID string) {
	s.mu.Lock()
	delete(s.starts, batchID)
	s.mu.Unlock()
}

func (s *Service) cancelSandboxStart(batchID string) {
	s.mu.Lock()
	cancel := s.starts[batchID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// A daemon that was down fires once, not once per missed slot.
func (s *Service) FireSchedules(ctx context.Context, now time.Time) {
	if s.Schedule == nil {
		return
	}
	envs, err := s.Store.ScheduledEnvironments(ctx)
	if err != nil {
		logx.Warn(err, "evidence.log.list_failed")
		return
	}
	for i := range envs {
		e := &envs[i]
		next, err := s.Schedule(e.Schedule, e.ScheduleTZ)
		if err != nil {
			continue
		}
		from := e.CreatedAt
		if e.LastScheduledAt != nil {
			from = *e.LastScheduledAt
		}
		if due := next(from); due.IsZero() || due.After(now) {
			continue
		}
		logx.Warn(s.Store.MarkScheduled(ctx, e.ID, now), "evidence.log.mark_failed", "env", e.ID)
		_, err = s.Store.CreateBatch(ctx, e.WorkspaceID, "schedule", BatchSpec{
			EnvironmentID: e.ID, Trigger: TriggerSchedule, TriggerRef: e.Schedule,
			ProjectID: e.ScheduleProjectID, Tag: e.ScheduleTag,
		})
		if err != nil && apperr.KindOf(err) != apperr.KindPrecondition {
			logx.Warn(err, "evidence.log.schedule_failed", "env", e.ID)
		}
	}
}

type Deploy struct {
	Repo        string
	Environment string
	SHA         string
	// Workspaces the repo's App installation belongs to; environments of any other workspace must
	// never fire.
	Workspaces []string
}

func DeploySlug(name string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '_' {
			return '-'
		}
		return r
	}, strings.ToLower(strings.TrimSpace(name)))
}

func deployMatches(e *Environment, name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	return e.Name == DeploySlug(name) || (e.DisplayName != "" && strings.EqualFold(e.DisplayName, strings.TrimSpace(name)))
}

func (s *Service) OnDeploy(ctx context.Context, d Deploy) int {
	if d.Repo == "" || len(d.Workspaces) == 0 {
		return 0
	}
	envs, err := s.Store.DeployEnvironments(ctx, d.Workspaces)
	if err != nil {
		logx.Warn(err, "evidence.log.list_failed")
		return 0
	}
	n := 0
	for i := range envs {
		e := &envs[i]
		repo, ok := GitHubRepo(e.RepoURL)
		if !ok || !strings.EqualFold(repo, d.Repo) || !deployMatches(e, d.Environment) {
			continue
		}
		ref := d.Repo
		if d.SHA != "" {
			ref += "@" + shortSHA(d.SHA)
		}
		ref += " · " + d.Environment
		if _, err := s.Store.CreateBatch(ctx, e.WorkspaceID, "deploy", BatchSpec{
			EnvironmentID: e.ID, Trigger: TriggerDeploy, TriggerRef: ref,
		}); err == nil {
			n++
		} else if apperr.KindOf(err) != apperr.KindPrecondition {
			logx.Warn(err, "evidence.log.deploy_failed", "env", e.ID)
		}
	}
	if n > 0 {
		s.Kick()
	}
	return n
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
