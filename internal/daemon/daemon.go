package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/api"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/edition"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/kubert"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
	"github.com/MisterDonnuts/thulu/internal/metrics"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/registry"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/singleton"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/syncx"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/usage"
	"github.com/MisterDonnuts/thulu/internal/watcher"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

type Config struct {
	ListenAddr string
	AgentsDir  string
	DataDir    string
}

func (c Config) dataDir() string {
	if c.DataDir != "" {
		return c.DataDir
	}
	return filepath.Join(c.AgentsDir, "octopus", "data")
}

func validateAuthStartup(listenAddr string) (string, error) {
	if auth.DevMode() && listenAddr == ":8585" && !auth.DevModeContainerBind() {
		// In a container, narrowing to loopback makes the published port unreachable, so the
		// acknowledged container case
		// keeps the wildcard bind and relies on a loopback-only publish (see
		// auth.DevModeContainerEnv).
		listenAddr = "127.0.0.1:8585"
	}
	if err := auth.ValidateStartupConfig(listenAddr); err != nil {
		return "", err
	}
	auth.ReportSecretPosture()
	if auth.DevMode() {
		confinement := i18n.Log("daemon.log.dev_mode_loopback_only")
		if auth.DevModeContainerBind() {
			confinement = i18n.Log("daemon.log.dev_mode_every_interface")
		}
		log.Printf("[daemon] %s", i18n.Log("daemon.log.dev_mode_warning", "addr", listenAddr, "confinement", confinement))
	}
	return listenAddr, nil
}

func loadDevSession(dataDir, listenAddr string) error {
	tok, err := auth.LoadDevToken(dataDir)
	if err != nil || tok == "" {
		return err
	}
	port := "8585"
	if _, p, splitErr := net.SplitHostPort(listenAddr); splitErr == nil && p != "" {
		port = p
	}
	log.Printf("[daemon] %s", i18n.Log("daemon.log.dev_mode_open", "url", auth.DevDashboardURL("http://localhost:"+port, tok)))
	return nil
}

func newMirroredRegistry(sharedDB *sql.DB) *registry.Registry {
	reg := registry.New()
	if regStore, err := registry.NewStore(sharedDB); err != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.registry_mirror_disabled", "err", err))
	} else {
		reg.AttachStore(regStore)
	}
	return reg
}

func prepareContainers(ctx context.Context, docker *client.Client, dataDir string) {
	setInstallID(dataDir)
	// Not inside adoptDataVolume: a nil *client.Client would arrive as a non-nil interface.
	if docker != nil {
		adoptDataVolume(ctx, docker, dataDir)
	}
}

func dialDocker(ctx context.Context) (*client.Client, error) {
	inK8s := strings.EqualFold(os.Getenv("OCTOPUS_AGENT_RUNTIME"), "kubernetes")
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		if !inK8s {
			return nil, err
		}
		log.Printf("[daemon] %s", i18n.Log("daemon.log.docker_client_unavailable", "err", err))
		return nil, nil
	}
	if _, pingErr := docker.Ping(ctx); pingErr != nil {
		if !inK8s {
			return nil, pingErr
		}
		log.Printf("[daemon] %s", i18n.Log("daemon.log.docker_unreachable", "err", pingErr))
		logx.Close(docker, i18n.Log("daemon.log.docker_client"))
		return nil, nil
	}
	log.Printf("[daemon] %s", i18n.Log("daemon.log.docker_connected"))
	return docker, nil
}

func Run(cfg Config) error {
	addr, err := validateAuthStartup(cfg.ListenAddr)
	if err != nil {
		return err
	}
	cfg.ListenAddr = addr

	if gateErr := checkStartupGates(); gateErr != nil {
		return gateErr
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	docker, err := dialDocker(ctx)
	if err != nil {
		return err
	}
	if docker != nil {
		defer logx.Close(docker, i18n.Log("daemon.log.docker_client"))
	}

	llmAuth := setupLLMAuth(cfg.AgentsDir)

	dataDir := cfg.dataDir()
	if devErr := loadDevSession(dataDir, cfg.ListenAddr); devErr != nil {
		return devErr
	}
	// Must run before anything creates or lists a container.
	prepareContainers(ctx, docker, dataDir)
	st, err := openStores(ctx, dataDir)
	if err != nil {
		return err
	}
	defer st.closeAll()

	reg := newMirroredRegistry(st.db)
	collector := usage.NewCollector(docker, st.usage, dataDir)
	w := watcher.New(docker, reg, collector, st.project)
	if docker != nil {
		if syncErr := w.SyncExisting(ctx); syncErr != nil {
			return syncErr
		}
	}
	st.startBackgroundLoops(ctx)

	kubeRT, browserCfg, kubeErr := buildAgentRuntimes()
	if kubeErr != nil {
		return kubeErr
	}
	sandboxMgr := buildSandboxManager(ctx, docker, kubeRT, st.sandbox, st.settings, dataDir)
	// One forwarder shared by the dispatcher and the chat runtime, so a /mcps registration reaches
	// both.
	mcpFwd := mcp.NewForwarder()
	chatSvc, chatBus := buildChatService(ctx, docker, kubeRT, st, dataDir, cfg.AgentsDir, mcpFwd, browserCfg)

	log.Printf("[daemon] %s", i18n.Log("daemon.log.agents_dir", "dir", cfg.AgentsDir))
	srv := api.New(ctx, api.Config{
		Addr: cfg.ListenAddr, AgentsDir: cfg.AgentsDir, RunsDir: runOutputsDir(dataDir),

		UsageStore: st.usage, ProjectStore: st.project, TaskStore: st.task,
		SettingsStore: st.settings, SandboxStore: st.sandbox, SecretsStore: st.secrets,
		AuthStore: st.auth, AuditStore: st.audit, WebhookStore: st.webhook,

		Docker: docker, Kube: kubeRT, SandboxManager: sandboxMgr,
		ChatService: chatSvc, Registry: reg, Watcher: w,

		ChatBus: chatBus, MCPForwarder: mcpFwd,
		Browser: browserCfg,
	})
	st.registerHealth(srv)
	st.wireOptional(ctx, srv)

	// Start the watcher only after api.New: before that the usage store cannot resolve a run's
	// workspace,
	// and sessions recorded without one are never visible to their tenant.
	if docker != nil {
		startBackgroundBackfills(ctx, st.usage, w)
		go w.Watch(ctx)
	} else {
		go reattributeSessions(ctx, st.usage)
	}

	sandboxMgr.Queue().SetConcurrencyPolicy(st.workspaceConcurrency)

	sandboxMgr.Queue().SetOnSlotGranted(func(taskID string, _ string) {
		if claimsOwnGrant(taskID) {
			return
		}
		t, err := st.task.Get(ctx, taskID)
		if err != nil {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.sandbox_grant_get_task_failed", "task", taskID, "err", err))
			return
		}
		if err := srv.Dispatcher().RelaunchStage(ctx, t, 0); err != nil {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.sandbox_grant_relaunch_failed", "task", taskID, "err", err))
			logx.Warn(sandboxMgr.Queue().Remove(ctx, taskID), "daemon.log.sandbox_queue_remove_failed", "task", taskID)
			_, addErr := st.task.AddNote(ctx, taskID, "", task.SourceSystem, "octopus",
				task.NoteOf("daemon.comments.sandbox_relaunch_failed", "err", err))
			logx.Warn(addErr, "daemon.log.sandbox_relaunch_note_failed", "task", taskID)
		}
	})

	go func() {
		<-ctx.Done()
		log.Printf("[daemon] %s", i18n.Log("daemon.log.shutting_down"))
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutCancel()
		//nolint:errcheck,contextcheck // shutdown error non-actionable; shutCtx is fresh (request ctx already canceled at this point)
		srv.Shutdown(shutCtx)
	}()

	if err := llmAuth.wire(ctx, srv); err != nil {
		return apperr.Wrap(err, "daemon.errors.llm_gateway")
	}

	go srv.ReconcileOrphans(ctx)

	startOperations(ctx, docker, kubeRT, sandboxMgr, st, srv, dataDir, cfg.AgentsDir)

	startSchedulers(ctx, srv)

	startProjectExcludesBackfill(ctx, st.project)

	startWorkspaceIDBackfill(ctx, st.db)

	log.Printf("[daemon] %s", i18n.Log("daemon.log.ready"))
	return srv.Start()
}

func startOperations(
	ctx context.Context,
	docker *client.Client,
	kubeRT *kubert.Runtime,
	sandboxMgr *sandbox.Manager,
	st *stores,
	srv *api.Server,
	dataDir, agentsDir string,
) {
	startCleanup(ctx, docker, kubeRT, sandboxMgr, st, srv, dataDir, agentsDir)
	startInfra(ctx, docker, kubeRT, st, srv, dataDir)
	if kubeRT != nil {
		startRoomWatch(ctx, kubeRT, srv)
		startImageBuild(ctx, kubeRT)
	}
	srv.StartSetupPrewarms(ctx)
}

func startCleanup(
	ctx context.Context,
	docker *client.Client,
	kubeRT *kubert.Runtime,
	sandboxMgr *sandbox.Manager,
	st *stores,
	srv *api.Server,
	dataDir, agentsDir string,
) {
	janitor := newJanitor(docker, sandboxMgr, st.task, newRetention(st, dataDir, agentsDir), dataDir)
	if st.agentStore != nil {
		janitor.agentImages = st.agentStore.Images
	}
	janitor.Start(ctx)
	hk := api.Housekeeping{Cleanup: janitor.Status}
	if dataDir != "" {
		// deploy/backup.sh writes its last run here; see write_status.
		hk.BackupStatusPath = filepath.Join(dataDir, "backup-status.json")
	}
	srv.SetHousekeeping(hk)
	if kubeRT == nil {
		if docker != nil {
			srv.SetTenantTeardown(func(ctx context.Context, workspaceID string) error {
				return sandbox.RemoveCacheVolumes(ctx, docker, workspaceID)
			})
		}
		return
	}
	startKubeCleanup(ctx, kubeRT, sandboxMgr, st, srv)
}

type stores struct {
	db *sql.DB
	// Held from before the first migration until shutdown, so a second daemon on the same database
	// waits instead of running boot work concurrently.
	lease     *singleton.Lease
	usage     *usage.Store
	project   *project.Store
	envMetric *envmetrics.Store
	ingest    *ingest.Store
	otlpDec   *ingest.OTLPDecoder
	// One shared instance for the API route and the sweep: two would each keep their own cooldown
	// and double the limit.
	envCollector *envmetrics.Collector
	task         *task.Store
	settings     *settings.Store
	sandbox      *sandbox.Store
	secrets      *secrets.Store
	creds        *credentials.Store
	mcpOAuth     *mcpoauth.Service
	chat         *chat.Store
	chatRT       *chatruntime.Store
	auth         *auth.UserStore
	audit        *audit.Store
	webhook      *webhook.Store
	entitle      *entitlements.Store
	mcpReg       *mcp.Registry
	agentStore   *agentstore.Store
	ghApp        *githubapp.Store
	stageReg     *stage.Store
	automations  *automation.Store
	evidence     *evidence.Store
	runner       *runner.Store
}

// Exits without the orderly shutdown on purpose: that takes up to 30s, and the replacement daemon
// may already be taking over.
func exitOnLostLease(ctx context.Context, lease *singleton.Lease) {
	select {
	case <-lease.Lost():
		log.Printf("[daemon] %s", i18n.Log("daemon.log.lease_lost_exit"))
		os.Exit(1)
	case <-ctx.Done():
	}
}

var dbWait = db.DefaultWait

func openStores(ctx context.Context, dataDir string) (st *stores, err error) {
	if mkErr := os.MkdirAll(dataDir, 0o750); mkErr != nil {
		return nil, apperr.Wrap(mkErr, "daemon.errors.create_data_dir", "dir", dataDir)
	}
	// The deferred close must hold its own pointer: error returns set the named st to nil, and
	// closing through it panics and hides the error.
	opened := &stores{}
	st = opened
	defer func() {
		if err != nil {
			opened.closeAll()
		}
	}()

	dsn := strings.TrimSpace(os.Getenv("OCTOPUS_DATABASE_URL"))
	if dsn == "" {
		err = apperr.Invalid("daemon.errors.database_url_required")
		return st, err
	}
	if st.db, err = db.Open(dsn); err != nil {
		err = apperr.Wrap(err, "daemon.errors.open_database")
		return st, err
	}
	// sql.Open does not connect; wait here before the first migration so a pod booting ahead of
	// Postgres or its NetworkPolicy does not crash-loop.
	if err = db.WaitReady(ctx, st.db, dbWait); err != nil {
		err = apperr.Wrap(err, "daemon.errors.open_database")
		return nil, err
	}
	if st.lease, err = singleton.Acquire(ctx, st.db, singleton.Defaults); err != nil {
		return nil, err
	}
	go exitOnLostLease(ctx, st.lease)
	log.Printf("[daemon] %s", i18n.Log("daemon.log.storage_backend_postgres"))

	if st.usage, err = usage.NewStore(st.db, dataDir); err != nil {
		return nil, err
	}
	wsDir := filepath.Join(dataDir, "projects")
	if err = os.MkdirAll(wsDir, 0o750); err != nil {
		return nil, apperr.Wrap(err, "daemon.errors.create_projects_dir", "dir", wsDir)
	}
	if st.project, err = project.NewStore(st.db, wsDir); err != nil {
		return nil, err
	}
	attachmentsDir := filepath.Join(dataDir, "task-attachments")
	if err = os.MkdirAll(attachmentsDir, 0o750); err != nil {
		return nil, apperr.Wrap(err, "daemon.errors.create_attachments_dir", "dir", attachmentsDir)
	}
	if st.task, err = task.NewStore(st.db, attachmentsDir); err != nil {
		return nil, err
	}

	defaultPlan := entitlements.PlanSelfHosted
	if auth.SaaSMode() {
		defaultPlan = entitlements.PlanFree
	}

	for _, open := range []func() error{
		func() (e error) { st.settings, e = settings.NewStore(st.db); return },
		func() (e error) { st.sandbox, e = sandbox.NewStore(st.db); return },
		func() (e error) { st.secrets, e = secrets.NewStore(st.db); return },
		func() (e error) { st.creds, e = credentials.NewStore(st.db); return },
		func() (e error) { st.mcpOAuth, e = mcpoauth.New(st.db, st.creds); return },
		func() (e error) { st.chat, e = chat.NewStore(st.db); return },
		func() (e error) { st.chatRT, e = chatruntime.NewStore(st.db); return },
		func() (e error) { st.auth, e = auth.NewUserStore(st.db); return },
		func() (e error) { st.audit, e = audit.NewStore(st.db); return },
		func() (e error) { st.webhook, e = webhook.NewStore(st.db); return },
		func() (e error) { st.entitle, e = entitlements.NewStore(st.db, defaultPlan); return },
		func() (e error) { st.mcpReg, e = mcp.NewRegistry(st.db); return },
		func() (e error) { st.agentStore, e = agentstore.New(st.db); return },
		func() (e error) { st.ghApp, e = githubapp.NewStore(st.db); return },
		func() (e error) { st.stageReg, e = stage.NewStore(st.db); return },
		func() (e error) { st.envMetric, e = envmetrics.NewStore(st.db); return },
		func() (e error) { st.ingest, e = ingest.NewStore(st.db); return },
		func() (e error) { st.automations, e = automation.NewStore(st.db); return },
		func() (e error) { st.evidence, e = evidence.NewStore(st.db); return },
		func() (e error) { st.runner, e = runner.NewStore(st.db); return },
		func() error { return tokenopt.Migrate(st.db) },
	} {
		// The explicit return assigns the named err, so the deferred closeAll still sees the
		// failure.
		if openErr := open(); openErr != nil {
			return nil, openErr
		}
	}
	// Must be set before anything reads the entitlements store.
	st.entitle.ExemptWorkspace(os.Getenv(api.OperatorWorkspaceEnv))
	st.syncCatalog(ctx)
	recordVersion(ctx, st.audit)
	// Must run after every store holding sealed secrets has migrated.
	if _, err := secretbox.MigrateAtStartup(ctx, st.db); err != nil {
		return nil, apperr.Wrap(err, "daemon.errors.reencrypt_secrets")
	}
	st.settings.SetStageBoard(st.stageReg.Catalog)
	if err := st.settings.SeedSandboxPriorities(ctx); err != nil {
		return nil, apperr.Wrap(err, "daemon.errors.seed_sandbox_priorities")
	}
	if n, err := st.project.MigrateLegacyStageCmds(ctx); err != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.legacy_stage_cmds_failed", "count", n, "err", err))
	} else if n > 0 {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.legacy_stage_cmds_migrated", "count", n))
	}
	// Not in NewStore: the CLI opens the store too, while this daemon may be cloning.
	st.project.FailInterruptedClones(ctx)
	log.Printf("[daemon] %s", i18n.Log("daemon.log.stores_opened", "dir", dataDir))
	return st, nil
}

// The nil check is required: a nil *sql.DB wrapped in io.Closer is not == nil, so logx.Close would
// dereference it.
func (st *stores) closeAll() {
	if st.lease != nil {
		st.lease.Release()
	}
	if st.db != nil {
		logx.Close(st.db, i18n.Log("daemon.log.octopus_db"))
	}
}

func (st *stores) registerHealth(srv *api.Server) {
	srv.SetEnvMetrics(st.envMetric)
	srv.SetEnvCollector(st.collector())
	srv.SetIngest(st.ingest, st.otlp())
	srv.RegisterHealthDB("usage", st.usage.DB())
	srv.RegisterHealthDB("projects", st.project.DB())
	srv.RegisterHealthDB("tasks", st.task.DB())
	srv.RegisterHealthDB("settings", st.settings.DB())
	srv.RegisterHealthDB("sandbox", st.sandbox.DB())
	srv.RegisterHealthDB("secrets", st.secrets.DB())
	srv.RegisterHealthDB("credentials", st.creds.DB())
	srv.RegisterHealthDB("chat", st.chat.DB())
	srv.RegisterHealthDB("chatruntime", st.chatRT.DB())
	srv.RegisterHealthDB("auth", st.auth.DB())
	srv.RegisterHealthDB("audit", st.audit.DB())
	srv.RegisterHealthDB("webhooks", st.webhook.DB())
	srv.RegisterHealthDB("stages", st.stageReg.DB())
	srv.RegisterHealthDB("automations", st.automations.DB())
	srv.RegisterHealthDB("evidence", st.evidence.DB())
}

func evidenceMaxRunners() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("OCTOPUS_EVIDENCE_MAX_RUNNERS")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Default off under Kubernetes: runners have not been tried there.
func runnersEnabled() bool {
	switch v := strings.TrimSpace(os.Getenv("OCTOPUS_RUNNERS")); {
	case envOn(v):
		return true
	case v == "0" || strings.EqualFold(v, "false"):
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("OCTOPUS_AGENT_RUNTIME")), "kubernetes")
}

func (st *stores) wireOptional(ctx context.Context, srv *api.Server) {
	srv.SetEntitlements(st.entitle)
	srv.SetCredentials(st.creds)
	srv.SetMCPOAuth(st.mcpOAuth)
	srv.SetMCPRegistry(st.mcpReg)
	srv.SetGitHubApp(githubapp.NewService(st.ghApp), st.ghApp)
	srv.SetStageStore(st.stageReg)
	srv.SetAgentStore(st.agentStore)
	autoEngine := automation.NewEngine(st.automations, srv.AutomationDeps())
	srv.SetAutomations(st.automations, autoEngine, automation.NewScheduler(st.automations, autoEngine))
	srv.SetEvidence(st.evidence, srv.NewEvidenceService(st.evidence, evidenceMaxRunners()))
	if runnersEnabled() {
		srv.SetRunners(st.runner)
	}
	st.task.WithExtraStorage(func(ctx context.Context, workspaceID string) (int64, error) {
		evidenceBytes, err := st.evidence.StorageBytes(ctx, workspaceID)
		if err != nil {
			return 0, err
		}
		codeBytes, err := st.project.HostedBytes(ctx, workspaceID)
		return evidenceBytes + codeBytes, err
	})
	srv.SetTokenOptimizer()
	startUpdateChecker(ctx, srv)
}

func (st *stores) runCredentialMonitor(ctx context.Context) {
	credentials.NewMonitor(st.creds, credentials.NewRegistry(), credentials.NewCatalog().Permissions()).
		OnChange(func(_ context.Context, ch credentials.Change) {
			level := "notice"
			if ch.Degraded() {
				level = "warning"
				log.Printf("[credentials] %s", ch.Summary())
			}
			st.audit.Log(audit.Entry{
				Kind:         audit.KindCredentialStateChanged,
				WorkspaceID:  ch.Credential.WorkspaceID,
				ActorID:      "system",
				ResourceID:   ch.Credential.ID,
				ResourceType: "credential",
				Summary:      ch.Summary(),
				Metadata: map[string]string{
					"provider": ch.Credential.Provider,
					"name":     ch.Credential.Name,
					"from":     string(ch.From),
					"to":       string(ch.To),
					"level":    level,
				},
			})
		}).
		Run(ctx)
}

func (st *stores) runRefreshTokenGC(ctx context.Context) {
	syncx.TickUntil(ctx, 6*time.Hour, func(context.Context) {
		if purged, purgeErr := st.auth.PurgeExpiredRefreshTokens(ctx); purgeErr != nil {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.refresh_token_gc_failed", "err", purgeErr))
		} else if purged > 0 {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.refresh_token_gc_purged", "count", purged))
		}
	})
}

func (st *stores) runMetricsRefresh(ctx context.Context) {
	syncx.TickUntil(ctx, 30*time.Second, func(context.Context) {
		if counts, err := st.task.CountByStage(ctx); err == nil {
			metrics.TasksTotal.Reset()
			for _, c := range counts {
				metrics.TasksTotal.WithLabelValues(c.BoardID, c.Stage).Set(float64(c.Count))
			}
		}
		if counts, err := st.task.CountByType(ctx); err == nil {
			metrics.TasksByType.Reset()
			for _, c := range counts {
				metrics.TasksByType.WithLabelValues(c.BoardID, c.TypeID).Set(float64(c.Count))
			}
		}
		if live, err := st.sandbox.CountLive(ctx); err == nil {
			metrics.SandboxesActive.Set(float64(live))
		}
	})
}

func reattributeSessions(ctx context.Context, usageStore *usage.Store) {
	if n, err := usageStore.ReattributeSessions(ctx); err != nil {
		log.Printf("[usage] %s", i18n.Log("daemon.log.reattribute_failed", "err", err))
	} else if n > 0 {
		log.Printf("[usage] %s", i18n.Log("daemon.log.reattributed", "count", n))
	}
}

func startBackgroundBackfills(ctx context.Context, usageStore *usage.Store, w *watcher.Watcher) {
	go func() {
		if n, err := usageStore.BackfillToolEvents(ctx); err != nil {
			log.Printf("[usage] %s", i18n.Log("daemon.log.backfill_tool_events_failed", "err", err))
		} else if n > 0 {
			log.Printf("[usage] %s", i18n.Log("daemon.log.backfilled_tool_events", "count", n))
		}
		if n, err := usageStore.BackfillAdoptOrphanSessions(ctx); err != nil {
			log.Printf("[usage] %s", i18n.Log("daemon.log.adopt_orphan_sessions_failed", "err", err))
		} else if n > 0 {
			log.Printf("[usage] %s", i18n.Log("daemon.log.adopted_orphan_sessions", "count", n))
		}
		// Must run last: it rewrites the tool events the two backfills above may have added.
		reattributeSessions(ctx, usageStore)
	}()
	go func() {
		if n, err := w.BackfillSessionLabels(ctx); err != nil {
			log.Printf("[watcher] %s", i18n.Log("daemon.log.backfill_session_labels_failed", "err", err))
		} else if n > 0 {
			log.Printf("[watcher] %s", i18n.Log("daemon.log.backfilled_session_labels", "count", n))
		}
	}()
}

func checkStartupGates() error {
	activateLicense()
	return checkDockerRuntimeSaaS(os.Getenv)
}

func activateLicense() {
	line := edition.Load()
	if line == "" {
		line = i18n.Log("licensing.log.community_edition")
	}
	log.Printf("[license] %s", line)
	// SaaS mode only tightens things, so it stays on without a license; what the license adds is
	// billing and more than one workspace.
	if auth.SaaSMode() && !edition.Has(edition.SaaS) {
		log.Printf("[license] %s", i18n.Log("licensing.log.saas_needs_license"))
	}
}

func kubeRuntimeRequested() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("OCTOPUS_AGENT_RUNTIME")), "kubernetes")
}

func buildAgentRuntimes() (*kubert.Runtime, browserrun.Config, error) {
	browserCfg, err := browserrun.ConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, browserrun.Config{}, err
	}
	log.Printf("[daemon] %s", i18n.Log("daemon.log.browser_mode", "mode", string(browserCfg.Mode), "image", browserCfg.Image))
	if !browserCfg.PerRun() && envOn(os.Getenv("OCTOPUS_SAAS_MODE")) {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.browser_shared_saas"))
	}
	kubeRT, err := buildKubeRuntime()
	return kubeRT, browserCfg, err
}

func buildChatService(
	ctx context.Context,
	docker *client.Client,
	kube *kubert.Runtime,
	st *stores,
	dataDir, agentsDir string,
	mcpFwd *mcp.Forwarder,
	browserCfg browserrun.Config,
) (*chatruntime.Service, *chat.Bus) {
	bus := chat.NewBus()
	apiBaseURL := strings.TrimSpace(os.Getenv("OCTOPUS_AGENT_BASE_URL"))
	chatRT := chatruntime.NewManager(st.chatRT, st.settings, docker, chatruntime.ManagerConfig{
		DataDir:      dataDir,
		DataVolume:   strings.TrimSpace(os.Getenv("OCTOPUS_DATA_VOLUME")),
		Kube:         kube,
		GitBaseURL:   os.Getenv("OCTOPUS_GIT_BASE_URL"),
		APIBaseURL:   apiBaseURL,
		MCPForwarder: mcpFwd,
		Browser:      browserCfg,
		WorkspaceLimit: func(workspaceID string) int {
			if workspaceID == "" || st.entitle == nil {
				return 0
			}
			ent, err := st.entitle.Get(workspaceID)
			if err != nil {
				return 0
			}
			return ent.ConcurrencyLimit
		},
	})
	code := func(taskID string) map[string]types.CodeView {
		t, err := st.task.Get(ctx, taskID)
		if err != nil {
			return nil
		}
		return task.ReadOnlyCode(ctx, t, st.project)
	}
	svc := chatruntime.NewService(st.chat, chatRT, bus, agentsDir, code)

	if docker != nil || kube != nil {
		chatRT.Reconcile(ctx)
		chatRT.StartGC(ctx, time.Minute)
	}
	return svc, bus
}

func claimsOwnGrant(owner string) bool {
	if _, ok := sandbox.PrewarmProjectID(owner); ok {
		return true
	}
	_, ok := sandbox.EvidenceBatchID(owner)
	return ok
}

func (st *stores) workspaceConcurrency(ctx context.Context, taskID string) (string, int) {
	wsID, err := st.ownerWorkspace(ctx, taskID)
	if err != nil || wsID == "" {
		return "", 0
	}
	ent, err := st.entitle.Get(wsID)
	if err != nil {
		return wsID, 0
	}
	return wsID, ent.ConcurrencyLimit
}

func (st *stores) ownerWorkspace(ctx context.Context, owner string) (string, error) {
	if projectID, ok := sandbox.PrewarmProjectID(owner); ok {
		p, err := st.project.Get(ctx, projectID)
		if err != nil {
			return "", err
		}
		return p.WorkspaceID, nil
	}
	if batchID, ok := sandbox.EvidenceBatchID(owner); ok {
		return st.evidence.BatchWorkspace(ctx, batchID)
	}
	t, err := st.task.Get(ctx, owner)
	if err != nil {
		return "", err
	}
	return t.WorkspaceID, nil
}

func (st *stores) runAuditRetentionGC(ctx context.Context) {
	purge := func() {
		raw, err := st.settings.GetRaw(ctx, "audit.retention_days")
		if err != nil || strings.TrimSpace(raw) == "" {
			return
		}
		days, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || days <= 0 {
			return
		}
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		if n, perr := st.audit.PurgeOlderThan(cutoff); perr != nil {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.audit_retention_gc_failed", "err", perr))
		} else if n > 0 {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.audit_retention_gc_purged", "count", n, "days", days))
		}
	}
	purge()
	syncx.TickUntil(ctx, 12*time.Hour, func(context.Context) { purge() })
}

func (st *stores) syncCatalog(ctx context.Context) {
	if st.agentStore == nil {
		return
	}
	written, err := st.agentStore.SyncSystemSkills(ctx, builtin.SystemSkills(), catalogActor)
	if err != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.system_skills_sync_failed", "err", err))
	}
	if len(written) > 0 {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.system_skills_synced", "skills", strings.Join(written, ", ")))
	}
	var pack agentstore.Pack
	if perr := json.Unmarshal(builtin.StarterPack(), &pack); perr != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.starter_pack_sync_failed", "err", perr))
	} else if changed, serr := st.agentStore.SyncStarterPack(ctx, pack, catalogActor); serr != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.starter_pack_sync_failed", "err", serr))
	} else if changed {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.starter_pack_synced", "revision", pack.Revision))
	}
	ids, err := st.auth.ListWorkspaceIDs(ctx)
	if err != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.legacy_adoption_failed", "err", err))
		return
	}
	if len(ids) == 0 {
		// Dev mode without workspaces runs everything as "default" (unrestrictedWorkspace).
		ids = []string{stage.FallbackWorkspaceID}
	}
	n, err := st.agentStore.AdoptLegacy(ctx, ids)
	if err != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.legacy_adoption_failed", "err", err))
	}
	if n > 0 {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.legacy_adopted", "count", n))
	}
}

const catalogActor = "octopus"

func (st *stores) runAgentContentVacuum(ctx context.Context) {
	if st.agentStore == nil {
		return
	}
	vacuum := func() {
		pruned, freed, err := st.agentStore.VacuumAll(ctx, agentstore.DefaultKeepPerFile)
		if err != nil {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.agent_content_vacuum_failed", "err", err))
			return
		}
		if pruned > 0 || freed > 0 {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.agent_content_vacuumed", "pruned", pruned, "freed", freed))
		}
	}
	vacuum()
	syncx.TickUntil(ctx, 24*time.Hour, func(context.Context) { vacuum() })
}

func buildSandboxManager(
	ctx context.Context,
	docker *client.Client,
	kubeRT *kubert.Runtime,
	sandboxStore *sandbox.Store,
	settingsStore *settings.Store,
	dataDir string,
) *sandbox.Manager {
	publicHost := os.Getenv("OCTOPUS_PUBLIC_HOST")
	if publicHost == "" {
		publicHost = "localhost"
	}
	var runtime sandbox.Runtime = sandbox.NewDocker(docker, sandbox.RuntimeImageFromEnv(os.Getenv))
	if kubeRT != nil {
		runtime = kubeSandboxRuntime(kubeRT)
	}
	dockerWrap := runtime
	memoryBytes := parseSandboxMemoryEnv(os.Getenv("OCTOPUS_SANDBOX_MEMORY_BYTES"))
	// With a shared data volume the manager must mount TypeVolume+Subpath, not bind daemon-internal
	// paths.
	// Keep in sync with the OCTOPUS_DATA_VOLUME wiring in internal/api/handle_tasks.go
	// (LaunchForStage).
	dataVolume := strings.TrimSpace(os.Getenv("OCTOPUS_DATA_VOLUME"))
	mgr := sandbox.NewManager(sandboxStore, settingsStore, dockerWrap, sandbox.ManagerConfig{
		PublicHost:  publicHost,
		MemoryBytes: memoryBytes,
		DataVolume:  dataVolume,
		DataRoot:    dataDir,
		AppPorts:    sandbox.AppPortsFromEnv(os.Getenv),
	})
	switch {
	case memoryBytes > 0:
		//nolint:gosec // G706: memoryBytes is a parsed int64, not user-controlled text — no log-injection vector
		log.Printf("[daemon] %s", i18n.Log("daemon.log.sandbox_memory_cap", "bytes", memoryBytes, "gb", float64(memoryBytes)/(1024*1024*1024)))
	case memoryBytes < 0:
		log.Printf("[daemon] %s", i18n.Log("daemon.log.sandbox_memory_cap_disabled"))
	default:
		log.Printf("[daemon] %s", i18n.Log("daemon.log.sandbox_memory_cap_default",
			"gb", float64(sandbox.DefaultSandboxMemoryBytes)/(1024*1024*1024)))
	}
	mgr.StartGC(ctx, 30*time.Second)
	return mgr
}

func parseSandboxMemoryEnv(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	low := strings.ToLower(s)
	if low == "0" || low == "off" || low == "none" || low == "disabled" {
		return -1
	}
	mult := int64(1)
	for suffix, m := range map[string]int64{
		"gb": 1024 * 1024 * 1024,
		"mb": 1024 * 1024,
		"kb": 1024,
	} {
		if strings.HasSuffix(low, suffix) {
			low = strings.TrimSuffix(low, suffix)
			mult = m
			break
		}
	}
	low = strings.TrimSpace(low)
	n, err := strconv.ParseInt(low, 10, 64)
	if err != nil || n < 0 {
		//nolint:gosec // G706: s is an operator-set env var, and %q already escapes control characters — no log-injection vector
		log.Printf("[daemon] %s", i18n.Log("daemon.log.sandbox_memory_invalid", "value", s))
		return 0
	}
	return n * mult
}

// Loops here must not depend on Docker: they also run in Kubernetes mode.
func (st *stores) startBackgroundLoops(ctx context.Context) {
	go st.runRefreshTokenGC(ctx)
	go st.runAuditRetentionGC(ctx)
	go st.runMetricsRefresh(ctx)
	go st.runCredentialMonitor(ctx)
	go st.runAgentContentVacuum(ctx)
	go st.ingest.RunFlusher(ctx, 30*time.Second)
	go st.otlp().RunFlusher(ctx, time.Minute)
	st.collector().Start(ctx)
}

// Must be one shared decoder: the HTTP handler fills in-memory buckets and the flusher drains them,
// so two instances would each write half the traffic.
func (st *stores) otlp() *ingest.OTLPDecoder {
	if st.otlpDec == nil {
		st.otlpDec = ingest.NewOTLPDecoder(st.envMetric)
		st.otlpDec.SetCustomStore(st.envMetric)
	}
	return st.otlpDec
}

func (st *stores) collector() *envmetrics.Collector {
	if st.envCollector == nil {
		st.envCollector = envmetrics.NewCollector(st.envMetric, st.settings, envMetricsCloudURL())
		if st.creds != nil {
			st.envCollector.SetTargets(envmetrics.NewTargets(st.project, st.creds))
		}
	}
	return st.envCollector
}

func envMetricsCloudURL() string {
	raw := strings.TrimSpace(os.Getenv("MCP_THULU_CLOUD_URL"))
	if raw == "" {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSuffix(raw, "/"), "/sse")
}

func startSchedulers(ctx context.Context, srv *api.Server) {
	srv.StartAutomations(ctx)
	srv.StartEvidence(ctx)
	srv.StartRunners(ctx)
}

func setInstallID(dataDir string) {
	installID, err := dockerx.LoadInstallID(dataDir)
	if err != nil {
		logx.Warn(err, "daemon.log.install_id_unavailable")
		return
	}
	dockerx.SetInstallID(installID)
}
