package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agentnet"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

// Without DOCKER_HOST, ask the CLI's current context: Colima and Docker Desktop expose their socket
// only there,
// and the SDK alone would dial a missing /var/run/docker.sock.
func OpenDocker() (*client.Client, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if os.Getenv("DOCKER_HOST") == "" {
		if host := contextHost(); host != "" {
			opts = append(opts, client.WithHost(host))
		}
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.docker_client")
	}
	return cli, nil
}

func contextHost() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect",
		"--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func envLookup(env *initwizard.EnvFile) func(string) string {
	return func(key string) string {
		if env != nil {
			if v := env.Get(key); v != "" {
				return v
			}
		}
		return os.Getenv(key)
	}
}

func (i *Install) Getenv() func(string) string {
	env, err := initwizard.LoadEnvFile(i.EnvPath())
	if err != nil {
		env = nil
	}
	return envLookup(env)
}

func (i *Install) GCOptions(ctx context.Context, api dockergc.API) (dockergc.Options, error) {
	env, err := initwizard.LoadEnvFile(i.EnvPath())
	if err != nil {
		return dockergc.Options{}, err
	}
	getenv := envLookup(env)
	opts := dockergc.Options{
		Keep:         dockergc.KeepFromEnv(getenv),
		Registries:   dockergc.RegistriesFromEnv(getenv),
		AgentNetwork: strings.TrimSpace(getenv(agentnet.EnvNetwork)),
	}
	// A rollback window written by an older updater is still honored until it ends.
	opts.Rollback, opts.RollbackUntil = dockergc.RollbackFromEnv(getenv)
	deployDir := filepath.Join(i.Root, "deploy")
	fallback := strings.ToLower(strings.TrimSpace(getenv("COMPOSE_PROJECT_NAME")))
	if fallback == "" {
		fallback = filepath.Base(deployDir)
	}
	opts.ComposeProjects, err = dockergc.DiscoverProjects(ctx, api, fallback, deployDir, filepath.Join(i.Root, "tools"))
	if err != nil {
		return dockergc.Options{}, err
	}
	return opts, nil
}

func (u *updateRun) pruneImages(ctx context.Context, what string) {
	api := u.opts.Docker
	if api == nil {
		cli, err := OpenDocker()
		if err != nil {
			u.say("updater.cli.docker_unreachable", "what", what, "err", apperr.Text(err))
			return
		}
		defer cli.Close()
		api = cli
	}
	opts, err := u.in.GCOptions(ctx, api)
	if err != nil {
		u.say("updater.cli.keep_set_unknown", "err", apperr.Text(err))
		return
	}
	opts.ImagesOnly = true
	plan, err := dockergc.Scan(ctx, api, opts)
	if err != nil {
		u.say("updater.cli.list_images_failed", "err", apperr.Text(err))
		return
	}
	if len(plan.Images) == 0 {
		return
	}
	res := dockergc.Apply(ctx, api, plan)
	u.say("updater.cli.images_removed", "count", res.Images, "what", what, "size", dockergc.HumanBytes(res.ImageBytes))
	if err := res.Err(); err != nil {
		u.say("updater.cli.some_not_removed", "err", apperr.Text(err))
	}
}

func (u *updateRun) pruneBackups(pinned ...string) {
	keep := BackupKeep(u.in.Getenv())
	removed, err := PruneBackups(filepath.Join(u.in.Root, "backups"), keep, pinned...)
	if err != nil {
		u.say("updater.cli.prune_snapshots_failed", "err", apperr.Text(err))
	}
	if len(removed) > 0 {
		u.say("updater.cli.snapshots_removed", "count", len(removed), "keep", keep)
	}
}
