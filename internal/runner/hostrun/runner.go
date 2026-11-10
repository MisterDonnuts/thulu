package hostrun

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

type Runner struct {
	cfg       *Config
	client    *Client
	repoLocks sync.Map
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
	bash := cfg.Bash
	if bash == "" {
		bash = "bash"
	}
	var missing []string
	for _, tool := range []string{bash, "git", "curl", "node", "claude"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return apperr.Precondition("hostrun.errors.missing_tools", "tools", strings.Join(missing, ", "))
	}
	return nil
}

const claimBackoff = 5 * time.Second

func (r *Runner) Run(ctx context.Context) error {
	if err := os.MkdirAll(r.cfg.WorkDir, 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", r.cfg.WorkDir)
	}
	sweepLeftovers(ctx, filepath.Join(r.cfg.WorkDir, "runs"))
	if len(r.cfg.Projects) == 0 {
		log.Printf("[runner] %s", i18n.Log("hostrun.log.no_projects"))
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.started", "name", r.cfg.Name, "daemon", r.cfg.DaemonURL,
		"projects", len(r.cfg.Projects), "concurrency", r.cfg.Concurrency))
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, r.cfg.Concurrency)
	for {
		select {
		case <-ctx.Done():
			return nil
		case slots <- struct{}{}:
		}
		resp, err := r.client.Claim(ctx, r.cfg.ProjectIDs())
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			j.execute(ctx)
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
	return &job{r: r, spec: resp.Job, every: every, root: filepath.Join(r.cfg.WorkDir, "runs", resp.Job.ID)}
}
