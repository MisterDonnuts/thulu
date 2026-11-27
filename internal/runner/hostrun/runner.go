package hostrun

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

type Runner struct {
	cfg       *Config
	client    *Client
	repoLocks sync.Map
	mcpList   mcpListCache
	// How the runner reads and signals processes; the zero value is this machine's (hostProcs).
	procs procOS

	mu sync.Mutex
	// The jobs this process is running, by id (janitor.go).
	jobs map[string]*job

	marksMu sync.Mutex
	// The run each process's environment names, read once per process (sweepMarked).
	marks map[procKey]runMark

	// Memory (memory.go). mem reads the machine's available memory and newSampler makes what reads a
	// run's processes; nil for this machine's own. scope says whether runs get a systemd scope.
	mem        memoryProbe
	newSampler func() memSampler
	scope      scopeSupport
	adm        admission
	// Asks reportFolders for a report now: the admission state changed.
	pushInv chan struct{}
	// The daemon answered 404 to the worktrees listing once; said in the log once (reclaim.go).
	worktreesUnsupported bool
}

func New(cfg *Config, version string) *Runner {
	return &Runner{cfg: cfg, client: NewClient(cfg.DaemonURL, cfg.Credential, version)}
}

func Register(ctx context.Context, path, daemonURL, token, version string) (*Config, error) {
	host, err := os.Hostname()
	logx.Warn(err, "hostrun.log.hostname_failed")
	resp, err := NewClient(daemonURL, "", version).Register(ctx, runner.RegisterRequest{
		Token: strings.TrimSpace(token), Hostname: host, Version: version,
	})
	if err != nil {
		return nil, err
	}
	cfg, lerr := LoadConfig(path)
	if lerr != nil {
		cfg = &Config{Projects: map[string]string{}}
	}
	cfg.DaemonURL = strings.TrimRight(daemonURL, "/")
	cfg.RunnerID, cfg.WorkspaceID, cfg.Name = resp.RunnerID, resp.WorkspaceID, resp.Name
	cfg.Credential = resp.Credential
	if err := cfg.Save(path); err != nil {
		return nil, err
	}
	return cfg, nil
}

func AddProject(path, projectID, dir string) error {
	cfg, err := LoadConfig(path)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.project_dir", "path", dir)
	}
	if _, err := git(context.Background(), abs, "rev-parse", "--git-dir"); err != nil {
		return apperr.Invalid("hostrun.errors.not_a_repo", "path", abs)
	}
	cfg.Projects[strings.TrimSpace(projectID)] = abs
	return cfg.Save(path)
}

func Preflight(cfg *Config) error {
	// Without both, a run's processes outlive it.
	if !processControl || !procTableSupported {
		return apperr.Precondition("hostrun.errors.unsupported_os", "os", runtime.GOOS)
	}
	bash := cfg.Bash
	if bash == "" {
		bash = "bash"
	}
	var missing []string
	for _, tool := range []string{bash, "git", "curl", "node"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return apperr.Precondition("hostrun.errors.missing_tools", "tools", strings.Join(missing, ", "))
	}
	if len(installedEngines()) == 0 {
		var binaries []string
		for _, name := range machineEngines() {
			binaries = append(binaries, engine.MachineLoginOf(name).Binary)
		}
		return apperr.Precondition("hostrun.errors.no_engine", "binaries", strings.Join(binaries, ", "))
	}
	return nil
}

// How long the runner waits after a failed claim, or before it checks the machine's memory again.
var claimBackoff = 5 * time.Second

func (r *Runner) Run(ctx context.Context) error {
	if err := os.MkdirAll(r.cfg.WorkDir, 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", r.cfg.WorkDir)
	}
	r.sweepAll(ctx)
	r.pushInv = make(chan struct{}, 1)
	if orDefault(r.cfg.RunMemoryLimitMB, DefaultRunMemoryLimitMB) > 0 {
		r.scope = probeScopes()
	}
	r.logMemory()
	roots, projects := r.cfg.folders()
	if len(projects) == 0 && len(roots) == 0 {
		log.Printf("[runner] %s", i18n.Log("hostrun.log.no_projects"))
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.started", "name", r.cfg.Name, "daemon", r.cfg.DaemonURL,
		"projects", len(projects), "concurrency", r.cfg.Concurrency))
	// A refusal from the daemon stops claiming in both pools; the jobs already taken finish.
	claimCtx, stopClaims := context.WithCancel(ctx)
	defer stopClaims()
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(3)
	go func() {
		defer wg.Done()
		r.reportFolders(claimCtx)
	}()
	go func() {
		defer wg.Done()
		r.janitor(claimCtx, janitorEvery)
	}()
	// On the runner's own context: the jobs already taken still run after a refusal stops claiming.
	go func() {
		defer wg.Done()
		r.memoryFloor(ctx, memEvery)
	}()
	pools := []claimPool{{kind: runner.ClaimRun, size: r.cfg.Concurrency}, {kind: runner.ClaimChat, size: r.cfg.ChatConcurrency}}
	errs := make(chan error, len(pools))
	for _, p := range pools {
		go func() {
			err := r.claimLoop(claimCtx, ctx, &wg, p)
			if err != nil {
				stopClaims()
			}
			errs <- err
		}()
	}
	var fatal error
	for range pools {
		if err := <-errs; err != nil && fatal == nil {
			fatal = err
		}
	}
	return fatal
}

type claimPool struct {
	kind string
	size int
}

// claimLoop takes the jobs of one pool while it has a free slot. Jobs run on jobCtx, so a refusal
// that stops claiming does not cut short the work already taken.
func (r *Runner) claimLoop(ctx, jobCtx context.Context, wg *sync.WaitGroup, p claimPool) error {
	slots := make(chan struct{}, max(p.size, 1))
	for {
		select {
		case <-ctx.Done():
			return nil
		case slots <- struct{}{}:
		}
		// Read again before each claim, so a folder allowed or a project mapped meanwhile counts
		// from the next run on.
		logx.Warn(r.cfg.Reload(), "hostrun.log.reload_failed")
		if !r.admit() {
			<-slots
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(claimBackoff):
			}
			continue
		}
		resp, err := r.client.Claim(ctx, r.cfg.ProjectIDs(), p.kind)
		if err != nil {
			<-slots
			if fatal := r.claimFailed(ctx, err); fatal != nil || ctx.Err() != nil {
				return fatal
			}
			continue
		}
		if resp.Job == nil {
			<-slots
			continue
		}
		j := r.newJob(resp)
		// Before any process of the job exists: the janitor kills those of jobs not running.
		r.addJob(j)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer r.dropJob(j.spec.ID)
			j.execute(jobCtx)
		}()
	}
}

func (r *Runner) claimFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	var ce *callError
	if asCallError(err, &ce) && (ce.Status == http.StatusUnauthorized || ce.Status == http.StatusPreconditionFailed) {
		return apperr.Precondition("hostrun.errors.refused", "err", ce.Body)
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.claim_failed", "err", err))
	select {
	case <-ctx.Done():
	case <-time.After(claimBackoff):
	}
	return nil
}

func (r *Runner) newJob(resp *runner.ClaimResponse) *job {
	every := time.Duration(resp.HeartbeatSeconds) * time.Second
	if every <= 0 {
		every = runner.DefaultHeartbeat
	}
	root := filepath.Join(r.cfg.WorkDir, "runs", resp.Job.ID)
	if resp.Job.ChatTurn() {
		root = r.cfg.chatRoot(resp.Job.ChatID)
	}
	lease := time.Duration(resp.LeaseSeconds) * time.Second
	if lease <= 0 {
		lease = runner.DefaultLease
	}
	return &job{r: r, spec: resp.Job, every: every, root: root, lease: lease,
		memLimit: r.cfg.current().runMemoryLimitMB(resp.Job)}
}
