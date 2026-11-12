package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/i18n"

	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/edition"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/eventhub"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/health"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/kubert"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
	"github.com/MisterDonnuts/thulu/internal/metrics"
	"github.com/MisterDonnuts/thulu/internal/middleware"
	"github.com/MisterDonnuts/thulu/internal/personallogin"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/registry"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/settings"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/usage"
	"github.com/MisterDonnuts/thulu/internal/watcher"
	"github.com/MisterDonnuts/thulu/internal/webhook"
	"github.com/MisterDonnuts/thulu/internal/workloadid"
	"github.com/MisterDonnuts/thulu/web"
)

type Server struct {
	mux *http.ServeMux

	registry            *registry.Registry
	docker              *client.Client
	personalLogins      *personallogin.Manager
	removeContainerHook func(ctx context.Context, c *infra.Container) error
	agentsDir           string
	runsDir             string
	agentCatalog        *types.Catalog
	agentStore          *agentstore.Store
	catalogs            sync.Map
	systemSkills        *systemSkillCache
	agentSource         types.AgentSource
	usageStore          *usage.Store
	projectStore        *project.Store
	taskStore           *task.Store
	taskDispatcher      *task.Dispatcher
	watcher             *watcher.Watcher
	settingsStore       *settings.Store
	boards              stagepkg.Boards
	stageAdmin          *stagepkg.Store
	sandbox             sandboxDeps
	secretsStore        *secrets.Store
	workloadIdentity    *workloadid.Issuer

	llmUpstreams map[string]string
	llmUsage     profileUsage
	tokenOpt     tokenOptDeps

	credStore     *credentials.Store
	credRegistry  *credentials.Registry
	credAuth      *credentials.Authorizer
	mcpOAuth      *mcpoauth.Service
	chatSvc       *chatruntime.Service
	chatBus       *chat.Bus
	evidenceStore *evidence.Store
	evidenceSvc   *evidence.Service
	runnerStore   *runner.Store
	runnerRoute   *routedLauncher
	authStore     *auth.UserStore
	auditStore    *audit.Store
	webhookStore  *webhook.Store
	webhookDisp   *webhook.Dispatcher
	entitlements  *entitlements.Store
	envMetrics    *envmetrics.Store
	envCollector  *envmetrics.Collector
	ingest        ingestDeps
	gh            ghDeps
	mcp           mcpDeps
	auto          autoDeps
	updates       updateDeps
	housekeeping  Housekeeping
	kube          *kubert.Runtime
	agentGit      agentGitState
	infra         *infra.Collector
	browser       browserrun.Config
	hub           *eventhub.Hub
	taskChanges   *task.ChangeBus
	healthChecker *health.Checker
	streamRecheck time.Duration
	spa           *spaHandler
	srv           *http.Server

	bgCancel context.CancelFunc

	tenantTeardown func(ctx context.Context, workspaceID string) error

	prewarm prewarmState

	// Store.Update is last-write-wins on the full row; without this lock concurrent
	// section/checklist ops clobber each other.
	sectionsMu sync.Mutex
}

type sandboxDeps struct {
	store *sandbox.Store
	mgr   *sandbox.Manager
	mcp   *sandbox.MCPHandler
}

type ingestDeps struct {
	store   *ingest.Store
	handler *ingest.Handler
}

type ghDeps struct {
	app    *githubapp.Service
	store  *githubapp.Store
	tokens githubTokenLookup
	prs    prStatusCache
}

type mcpDeps struct {
	mgr        mcpController
	registry   *mcp.Registry
	fwd        *mcp.Forwarder
	prober     mcpProber
	toolsCache toolsCache
	env        map[string]string
	envForward map[string]bool
	proxy      mcpProxyDeps
	detector   *mcp.Detector
}

type mcpProber interface {
	Tools(ctx context.Context, sseURL string) ([]mcp.ToolInfo, error)
	Ping(ctx context.Context, sseURL string) error
}

type autoDeps struct {
	store     *automation.Store
	engine    *automation.Engine
	scheduler *automation.Scheduler
}

func (s *Server) SetEntitlements(e *entitlements.Store) { s.entitlements = e }

func (s *Server) SetAutomations(store *automation.Store, engine *automation.Engine, sched *automation.Scheduler) {
	s.auto.store, s.auto.engine, s.auto.scheduler = store, engine, sched
}

func (s *Server) StartAutomations(ctx context.Context) {
	if s.auto.scheduler == nil {
		return
	}
	s.auto.scheduler.Start(ctx)
}

func (s *Server) AutomationPublisher() automation.Publisher {
	if s.auto.scheduler == nil {
		return nil
	}
	return s.auto.scheduler
}

func (s *Server) SetCredentials(store *credentials.Store) {
	s.credStore = store
	if store == nil {
		s.credRegistry, s.credAuth = nil, nil
		s.rewireMCPProxyAuth()
		s.rewireGithubResolver()
		return
	}
	s.credRegistry = credentials.NewRegistry()
	s.credAuth = credentials.NewAuthorizer(store, credentials.NewCatalog())
	s.rewireMCPProxyAuth()
	s.rewireGithubResolver()
}

// Reads s.credAuth per call so wireDispatcher can install it before SetCredentials runs.
func (s *Server) credentialGate(ctx context.Context, t *task.Task) ([]credentials.Fault, error) {
	if s.credAuth == nil || t == nil {
		return nil, nil
	}
	return s.credAuth.CheckDispatch(ctx, credentials.DispatchRequest{
		Reach:      s.reachForTask(ctx, t),
		Stage:      string(t.Stage),
		MCPEntries: s.stageMCPEntries(t),
	})
}

func (s *Server) stageMCPEntries(t *task.Task) []string {
	if s.taskDispatcher == nil {
		return nil
	}
	agentName := t.AssignedAgent(t.Stage)
	if agentName == "" {
		return nil
	}
	at := s.taskDispatcher.FindAgentTypeForRelaunch(t.WorkspaceID, agentName)
	if at == nil || at.MCPByStage == nil {
		return nil
	}
	entries, ok := at.MCPByStage[string(t.Stage)]
	if !ok {
		return nil
	}
	return entries
}

func (s *Server) SetEnvMetrics(e *envmetrics.Store) { s.envMetrics = e }

func (s *Server) SetEnvCollector(c *envmetrics.Collector) { s.envCollector = c }

func (s *Server) SetIngest(store *ingest.Store, decoder ingest.Decoder) {
	if store == nil {
		return
	}
	s.ingest.store = store
	s.ingest.handler = ingest.NewHandler(store, decoder)
}

func (s *Server) SetStageStore(store *stagepkg.Store) {
	if store == nil {
		return
	}
	s.stageAdmin = store
	s.SetBoards(store)
}

func (s *Server) SetStageRegistry(r stagepkg.Registry) {
	if r == nil {
		return
	}
	s.SetBoards(stagepkg.SingleBoard(r))
}

func (s *Server) SetBoards(b stagepkg.Boards) {
	if b == nil {
		return
	}
	s.boards = b
	if s.taskDispatcher != nil {
		s.taskDispatcher.WithBoards(b)
	}
	if s.taskStore != nil {
		s.taskStore.WithBoards(b)
	}
}

func (s *Server) stagesFor(t *task.Task) stagepkg.Registry {
	if t == nil {
		return s.boards.Board(s.boards.DefaultBoardID())
	}
	return s.boards.BoardFor(t.BoardID, t.TypeID)
}

// Deliberately unscoped: narrowing to the caller's boards makes other workspaces' stages look
// orphaned and the prune endpoints delete config in use.
func (s *Server) catalogStages() []types.StageInfo {
	return append(s.boards.Catalog(), stagepkg.Virtuals()...)
}

func (s *Server) catalogHas(id types.Stage) bool {
	catalog := s.catalogStages()
	for i := range catalog {
		if catalog[i].ID == id {
			return true
		}
	}
	return false
}

func (s *Server) SetMCPRegistry(r *mcp.Registry) {
	s.mcp.registry = r
	if r != nil && s.mcp.fwd != nil {
		s.mcp.fwd.SetGrantRecorder(r)
	}
	if r == nil || s.taskDispatcher == nil {
		return
	}
	regs, err := r.List(context.Background())
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.server.log.mcp_registry_seed_failed", "err", err))
		return
	}
	for i := range regs {
		s.forwardRegistration(&regs[i])
	}
}

type Config struct {
	Addr string

	AgentsDir string
	RunsDir   string

	UsageStore    *usage.Store
	ProjectStore  *project.Store
	TaskStore     *task.Store
	SettingsStore *settings.Store
	SandboxStore  *sandbox.Store
	SecretsStore  *secrets.Store
	AuthStore     *auth.UserStore
	AuditStore    *audit.Store
	WebhookStore  *webhook.Store

	Docker         *client.Client
	Kube           *kubert.Runtime
	SandboxManager *sandbox.Manager
	ChatService    *chatruntime.Service
	Registry       *registry.Registry
	Watcher        *watcher.Watcher

	ChatBus      *chat.Bus
	MCPForwarder *mcp.Forwarder

	Browser browserrun.Config
}

func New(ctx context.Context, cfg Config) *Server {
	s := &Server{
		registry:      cfg.Registry,
		docker:        cfg.Docker,
		agentsDir:     cfg.AgentsDir,
		runsDir:       cfg.RunsDir,
		agentCatalog:  types.NewCatalog(cfg.AgentsDir),
		usageStore:    cfg.UsageStore,
		projectStore:  cfg.ProjectStore,
		taskStore:     cfg.TaskStore,
		watcher:       cfg.Watcher,
		settingsStore: cfg.SettingsStore,
		boards:        stagepkg.StaticBoards(),
		sandbox:       sandboxDeps{store: cfg.SandboxStore, mgr: cfg.SandboxManager},
		secretsStore:  cfg.SecretsStore,
		chatSvc:       cfg.ChatService,
		chatBus:       cfg.ChatBus,
		authStore:     cfg.AuthStore,
		auditStore:    cfg.AuditStore,
		webhookStore:  cfg.WebhookStore,
		browser:       cfg.Browser,
	}
	if cfg.UsageStore != nil {
		cfg.UsageStore.WithWorkspaceResolver(s.sessionWorkspace)
	}
	if cfg.SecretsStore != nil {
		s.workloadIdentity = workloadid.New(cfg.SecretsStore, os.Getenv)
	}

	bgCtx, bgCancel := context.WithCancel(ctx)
	s.bgCancel = bgCancel
	s.hub = eventhub.New(bgCtx, s.resolveTopic)
	s.taskChanges = task.NewChangeBus()
	if cfg.TaskStore != nil {
		cfg.TaskStore.WithChangeBus(s.taskChanges)
	}
	if cfg.WebhookStore != nil {
		s.webhookDisp = webhook.NewDispatcher(bgCtx, cfg.WebhookStore)
	}
	s.wireDispatcher(ctx, cfg.Kube, cfg.MCPForwarder)
	s.wireApprovals()
	if s.chatSvc != nil {
		s.chatSvc.WithAgentSource(serverAgents{s}).WithPromptCheck(s.agentHasPrompt).WithAgentAugment(s.evidenceAugment)
	}
	s.healthChecker = health.New(cfg.Docker, nil)
	s.healthChecker.SetDetailGate(s.healthDetailAllowed)
	s.wireMCP(cfg.Kube)

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.srv = &http.Server{
		Addr:    cfg.Addr,
		Handler: s.buildHandler(mux),
		// Only the header timeout: ReadTimeout/WriteTimeout would cut SSE streams and 50 MB
		// uploads.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

type mcpController interface {
	List(ctx context.Context) ([]mcp.Service, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string) error
}

type roleGate = func(auth.Role, http.HandlerFunc) http.HandlerFunc

// confineRunKey must sit inside the role gate so it sees the authenticated key.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	s.mux = mux
	rr := func(role auth.Role, h http.HandlerFunc) http.HandlerFunc {
		return s.holdForConfirmation(auth.RequireRole(role, s.confineRunKey(h)))
	}
	s.registerSystemRoutes(mux)
	s.registerAuthRoutes(mux)
	s.registerCatalogRoutes(mux, rr)
	s.registerWorkshopRoutes(mux, rr)
	s.registerAgentRoutes(mux, rr)
	s.registerProjectRoutes(mux, rr)
	s.registerUploadProjectRoutes(mux, rr)
	s.registerTaskRoutes(mux, rr)
	s.registerMCPProxyRoutes(mux, rr)
	s.registerQuestionRoutes(mux, rr)
	s.registerApprovalRoutes(mux, rr)
	s.registerOpsRoutes(mux, rr)
	s.registerGitRoutes(mux)
}

func (s *Server) registerSystemRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.healthChecker.HandleFull)
	mux.HandleFunc("GET /health/live", s.healthChecker.HandleLive)
	mux.HandleFunc("GET /health/ready", s.healthChecker.HandleReady)
	mux.HandleFunc("GET /openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /info", s.handleInfo)
	metrics.Init()
	mux.HandleFunc("GET /metrics", s.gateMetrics(s.withHubStats(metrics.Handler())))
	// Public on purpose: authenticated by the Stripe signature inside the handler.
	mux.HandleFunc("POST /billing/stripe/webhook", s.handleStripeWebhook)
	// Public on purpose: the webhook checks X-Hub-Signature-256, the redirects verify the state
	// token or re-fetch from GitHub.
	mux.HandleFunc("POST /github/webhook", s.handleGithubWebhook)
	mux.HandleFunc("GET /github/setup", s.handleGithubSetup)
	mux.HandleFunc("GET /github/oauth/callback", s.handleGithubOAuthCallback)
	mux.HandleFunc("GET /github/manifest/callback", s.handleGithubManifestCallback)
	// Public on purpose: the model provider fetches these to verify run tokens. Public keys only.
	mux.HandleFunc("GET "+workloadid.DiscoveryPath,
		gated(s.workloadIdentity != nil, "api.server.errors.secrets_store_unavailable", s.handleOIDCDiscovery))
	mux.HandleFunc("GET "+workloadid.JWKSPath,
		gated(s.workloadIdentity != nil, "api.server.errors.secrets_store_unavailable", s.handleJWKS))
	s.spa = newSPAHandler(web.DistFS)
	mux.Handle("/", s.spa)
}

func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	if s.authStore == nil {
		return
	}
	authAuditFn := auth.AuditFn(func(e auth.AuditEvent) {
		if s.auditStore == nil {
			return
		}
		resourceType := e.ResourceType
		if resourceType == "" {
			resourceType = "user"
		}
		s.auditStore.Log(audit.Entry{
			Kind:         audit.EventKind(e.Kind),
			WorkspaceID:  e.WorkspaceID,
			ActorID:      e.Actor.ID,
			ActorType:    e.Actor.Type,
			ActorLabel:   e.Actor.Label,
			ActorEmail:   e.Actor.Email,
			ResourceID:   e.ResourceID,
			ResourceType: resourceType,
			ClientIP:     e.ClientIP,
			UserAgent:    e.UserAgent,
			Summary:      e.Summary,
		})
	})
	authHandler := auth.NewHandler(s.authStore).WithAudit(authAuditFn).WithEventCounter(metrics.RecordAuthEvent)
	// The resolver reads s.entitlements at call time because SetEntitlements runs after New.
	authHandler = authHandler.WithSeatLimit(s.seatLimitForWorkspace)
	authHandler = authHandler.WithWorkspacePurge(s.purgeWorkspace)
	authHandler = authHandler.WithTaskTitles(s.taskTitles).WithStageLabels(s.runStageLabel)
	if ssoProviders, err := auth.LoadSSOProviders(context.Background()); err != nil {
		log.Printf("[auth] %s", i18n.Log("api.server.log.sso_load_failed", "err", err))
	} else if len(ssoProviders) > 0 {
		authHandler = authHandler.WithSSO(ssoProviders)
		log.Printf("[auth] %s", i18n.Log("api.server.log.sso_loaded", "count", len(ssoProviders)))
	}
	authHandler.RegisterRoutes(mux)
}

func (s *Server) taskTitles(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	if s.taskStore == nil {
		return out
	}
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		if t, err := s.taskStore.Get(ctx, id); err == nil && t != nil {
			out[id] = t.Title
		}
	}
	return out
}

func (s *Server) runStageLabel(ctx context.Context, taskID, stage string) string {
	var t *task.Task
	if s.taskStore != nil && taskID != "" {
		if got, err := s.taskStore.Get(ctx, taskID); err == nil {
			t = got
		}
	}
	info, ok := s.stagesFor(t).Get(types.Stage(stage))
	if !ok {
		return ""
	}
	return stagepkg.LocalizeSeeded([]types.StageInfo{info}, i18n.FromContext(ctx))[0].Label
}

func (s *Server) registerCatalogRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /types", rr(auth.RoleViewer, s.handleListTypes))
	mux.HandleFunc("POST /types", rr(auth.RoleAdmin, s.handleCreateAgent))
	mux.HandleFunc("DELETE /types/{name}", rr(auth.RoleAdmin, s.handleDeleteAgent))
	mux.HandleFunc("GET /types/{name}/files", rr(auth.RoleViewer, s.handleListAgentFiles))
	mux.HandleFunc("GET /types/{name}/files/{path...}", rr(auth.RoleViewer, s.handleReadAgentFile))
	mux.HandleFunc("PUT /types/{name}/files/{path...}", rr(auth.RoleAdmin, s.handleWriteAgentFile))
	mux.HandleFunc("DELETE /types/{name}/files/{path...}", rr(auth.RoleAdmin, s.handleDeleteAgentFile))
	mux.HandleFunc("GET /types/{name}/revisions", rr(auth.RoleViewer, s.handleAgentRevisions))
	mux.HandleFunc("GET /types/{name}/revisions/{id}/body", rr(auth.RoleViewer, s.handleAgentRevisionBody))
	mux.HandleFunc("POST /types/{name}/revisions/{id}/restore", rr(auth.RoleAdmin, s.handleRestoreAgentRevision))
	mux.HandleFunc("GET /types/{name}/definition-revisions", rr(auth.RoleViewer, s.handleAgentDefRevisions))
	mux.HandleFunc("POST /types/{name}/definition-revisions/{id}/restore", rr(auth.RoleAdmin, s.handleRestoreAgentDefRevision))
	mux.HandleFunc("GET /types/{name}/settings", rr(auth.RoleViewer, s.handleGetAgentSettings))
	mux.HandleFunc("PUT /types/{name}/settings", rr(auth.RoleAdmin, s.handlePutAgentSettings))
	mux.HandleFunc("POST /types/{name}/settings/prune-stages", rr(auth.RoleAdmin, s.handlePruneAgentStages))
	mux.HandleFunc("GET /types/{name}/prompts", rr(auth.RoleViewer, s.handleListAgentPrompts))
	mux.HandleFunc("GET /types/{name}/usage", rr(auth.RoleViewer, s.handleAgentUsage))
	mux.HandleFunc("GET /boards", rr(auth.RoleViewer, s.handleListBoards))
	mux.HandleFunc("POST /boards", rr(auth.RoleAdmin, s.handleCreateBoard))
	mux.HandleFunc("PATCH /boards/{id}", rr(auth.RoleAdmin, s.handleUpdateBoard))
	mux.HandleFunc("DELETE /boards/{id}", rr(auth.RoleAdmin, s.handleDeleteBoard))
	mux.HandleFunc("GET /boards/{id}/stages", rr(auth.RoleViewer, s.handleListBoardStages))
	mux.HandleFunc("GET /task-types", rr(auth.RoleViewer, s.handleListTaskTypes))
	mux.HandleFunc("GET /task-types/icons", rr(auth.RoleViewer, s.handleListTaskTypeIcons))
	mux.HandleFunc("GET /boards/{id}/types", rr(auth.RoleViewer, s.handleListBoardTypes))
	mux.HandleFunc("PUT /boards/{id}/types", rr(auth.RoleAdmin, s.handleSetBoardTypes))
	mux.HandleFunc("GET /stages", rr(auth.RoleViewer, s.handleListStages))
	mux.HandleFunc("POST /stages", rr(auth.RoleAdmin, s.handleCreateStage))
	mux.HandleFunc("PUT /stages/order", rr(auth.RoleAdmin, s.handleReorderStages))
	mux.HandleFunc("PATCH /stages/{id}", rr(auth.RoleAdmin, s.handleUpdateStage))
	mux.HandleFunc("DELETE /stages/{id}", rr(auth.RoleAdmin, s.handleDeleteStage))
	mux.HandleFunc("GET /skills", rr(auth.RoleViewer, s.handleListSkills))
	mux.HandleFunc("POST /skills", rr(auth.RoleAdmin, s.handleCreateSkill))
	mux.HandleFunc("DELETE /skills/{name}", rr(auth.RoleAdmin, s.handleDeleteSkill))
	mux.HandleFunc("PATCH /skills/{name}", rr(auth.RoleAdmin, s.handlePatchSkill))
	mux.HandleFunc("GET /skills/deleted", rr(auth.RoleViewer, s.handleListDeletedSkills))
	mux.HandleFunc("POST /skills/{name}/restore", rr(auth.RoleAdmin, s.handleRestoreSkill))
	mux.HandleFunc("GET /skills/{name}/usage", rr(auth.RoleViewer, s.handleSkillUsage))
	mux.HandleFunc("GET /skills/{name}/files", rr(auth.RoleViewer, s.handleListSkillFiles))
	mux.HandleFunc("GET /skills/{name}/files/{path...}", rr(auth.RoleViewer, s.handleReadSkillFile))
	mux.HandleFunc("PUT /skills/{name}/files/{path...}", rr(auth.RoleAdmin, s.handleWriteSkillFile))
	mux.HandleFunc("DELETE /skills/{name}/files/{path...}", rr(auth.RoleAdmin, s.handleDeleteSkillFile))
	mux.HandleFunc("GET /skills/{name}/revisions", rr(auth.RoleViewer, s.handleSkillRevisions))
	mux.HandleFunc("GET /skills/{name}/revisions/{id}/body", rr(auth.RoleViewer, s.handleSkillRevisionBody))
	mux.HandleFunc("POST /skills/{name}/revisions/{id}/restore", rr(auth.RoleAdmin, s.handleRestoreSkillRevision))
	mux.HandleFunc("GET /system/skills", rr(auth.RoleViewer, s.handleListSystemSkills))
	mux.HandleFunc("GET /system/skills/{name}/files", rr(auth.RoleViewer, s.handleListSystemSkillFiles))
	mux.HandleFunc("GET /system/skills/{name}/files/{path...}", rr(auth.RoleViewer, s.handleReadSystemSkillFile))
	mux.HandleFunc("PUT /system/skills/{name}", rr(auth.RoleAdmin, s.handlePutSystemSkill))
	mux.HandleFunc("PUT /system/skills/{name}/files/{path...}", rr(auth.RoleAdmin, s.handlePutSystemSkillFile))
	mux.HandleFunc("DELETE /system/skills/{name}", rr(auth.RoleAdmin, s.handleDeleteSystemSkill))
	mux.HandleFunc("GET /tools", rr(auth.RoleViewer, s.handleListTools))
}

func (s *Server) registerAgentRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /agents", rr(auth.RoleViewer, s.handleListAgents))
	mux.HandleFunc("GET /agents/{id}", rr(auth.RoleViewer, s.handleGetAgent))
	mux.HandleFunc("POST /agents/{id}/stop", rr(auth.RoleDeveloper, s.handleStopAgent))
	mux.HandleFunc("DELETE /agents/{id}", rr(auth.RoleAdmin, s.handleRemoveAgent))
	mux.HandleFunc("GET /agents/{id}/files", rr(auth.RoleViewer, s.handleListFiles))
	mux.HandleFunc("GET /agents/{id}/files/{path...}", rr(auth.RoleViewer, s.handleServeFile))
	mux.HandleFunc("GET /agents/{id}/logs", rr(auth.RoleViewer, s.handleLogs))

	mux.HandleFunc("GET /usage", rr(auth.RoleViewer, s.handleUsage))
	mux.HandleFunc("GET /token-optimizer/workers", rr(auth.RoleViewer, s.handleTokenOptimizerWorkers))
	mux.HandleFunc("GET /token-optimizer/resolve", rr(auth.RoleViewer, s.handleResolveTokenOptimizer))
	mux.HandleFunc("GET /usage/sessions", rr(auth.RoleViewer, s.handleUsageSessions))
	mux.HandleFunc("GET /usage/sessions/{id}/logs", rr(auth.RoleViewer, s.handleSessionLogs))
	mux.HandleFunc("GET /usage/sessions/{id}/logs/stream", rr(auth.RoleViewer, s.handleSessionLogsStream))
	mux.HandleFunc("GET /usage/sessions/{id}/files", rr(auth.RoleViewer, s.handleSessionFiles))
	mux.HandleFunc("GET /usage/sessions/{id}/files/{path...}", rr(auth.RoleViewer, s.handleSessionServeFile))
	mux.HandleFunc("GET /usage/sessions/{id}/tool-events", rr(auth.RoleViewer, s.handleUsageSessionToolEvents))
	mux.HandleFunc("GET /usage/sessions/{id}/breakdown", rr(auth.RoleViewer, s.handleUsageSessionBreakdown))

	mux.HandleFunc("GET /usage/timeseries", rr(auth.RoleViewer, s.handleUsageTimeseries))
	mux.HandleFunc("GET /usage/by-engine", rr(auth.RoleViewer, s.handleUsageByDimension("engine")))
	mux.HandleFunc("GET /usage/by-model", rr(auth.RoleViewer, s.handleUsageByDimension("model")))
	mux.HandleFunc("GET /usage/by-agent-type", rr(auth.RoleViewer, s.handleUsageByDimension("agent_type")))
	mux.HandleFunc("GET /usage/by-project", rr(auth.RoleViewer, s.handleUsageByDimension("project_id")))
	mux.HandleFunc("GET /usage/by-stage", rr(auth.RoleViewer, s.handleUsageByDimension("stage")))
	mux.HandleFunc("GET /usage/by-service-tier", rr(auth.RoleViewer, s.handleUsageByDimension("service_tier")))
	mux.HandleFunc("GET /usage/by-tool", rr(auth.RoleViewer, s.handleUsageByTool))
	mux.HandleFunc("GET /usage/by-skill", rr(auth.RoleViewer, s.handleUsageBySkill))
	mux.HandleFunc("GET /usage/by-mcp-server", rr(auth.RoleViewer, s.handleUsageByMCPServer))
	mux.HandleFunc("GET /usage/token-optimizer", rr(auth.RoleViewer, s.handleUsageByTokenOptimizer))
	mux.HandleFunc("GET /usage/delegations", rr(auth.RoleViewer, s.handleUsageDelegations))
	mux.HandleFunc("GET /usage/top-tasks", rr(auth.RoleViewer, s.handleUsageTopTasks))
	mux.HandleFunc("GET /usage/efficiency", rr(auth.RoleViewer, s.handleUsageEfficiency))
	mux.HandleFunc("GET /usage/burn-rate", rr(auth.RoleViewer, s.handleUsageBurnRate))
	mux.HandleFunc("GET /usage/filtered-sessions", rr(auth.RoleViewer, s.handleUsageFilteredSessions))
	mux.HandleFunc("GET /usage/pricing", rr(auth.RoleViewer, s.handleUsagePricing))
}

func (s *Server) registerUploadProjectRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("POST /projects/upload", rr(auth.RoleAdmin, s.handleUploadProject))
	mux.HandleFunc("POST /projects/{id}/upload", rr(auth.RoleDeveloper, s.handleReuploadProject))
	mux.HandleFunc("GET /projects/{id}/branches", rr(auth.RoleViewer, s.handleProjectBranches))
	mux.HandleFunc("GET /projects/{id}/archive", rr(auth.RoleViewer, s.handleProjectArchive))
}

func (s *Server) registerGithubAppRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /github/app", rr(auth.RoleViewer, s.handleGithubAppStatus))
	mux.HandleFunc("PUT /github/app", rr(auth.RoleOwner, s.handleSetGithubApp))
	mux.HandleFunc("DELETE /github/app", rr(auth.RoleOwner, s.handleDeleteGithubApp))
	mux.HandleFunc("GET /github/app/install-link", rr(auth.RoleAdmin, s.handleGithubInstallLink))
	mux.HandleFunc("GET /github/app/manifest", rr(auth.RoleOwner, s.handleGithubAppManifest))
	mux.HandleFunc("GET /github/installations", rr(auth.RoleViewer, s.handleListGithubInstallations))
	mux.HandleFunc("GET /github/installations/{id}/repositories", rr(auth.RoleViewer, s.handleListInstallationRepos))
	mux.HandleFunc("DELETE /github/installations/{id}", rr(auth.RoleAdmin, s.handleUnlinkGithubInstallation))
	mux.HandleFunc("PUT /github/installations/{id}/workspace", rr(auth.RoleOwner, s.handleReassignGithubInstallation))
}

func (s *Server) registerProjectRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /projects", rr(auth.RoleViewer, s.handleListProjects))
	mux.HandleFunc("POST /projects", rr(auth.RoleAdmin, s.handleCreateProject))
	mux.HandleFunc("GET /projects/{id}", rr(auth.RoleViewer, s.handleGetProject))
	mux.HandleFunc("PATCH /projects/{id}", rr(auth.RoleAdmin, s.handleUpdateProject))
	mux.HandleFunc("DELETE /projects/{id}", rr(auth.RoleAdmin, s.handleDeleteProject))
	mux.HandleFunc("GET /projects/{id}/worktrees", rr(auth.RoleViewer, s.handleListWorktrees))
	mux.HandleFunc("GET /projects/{id}/metrics", rr(auth.RoleViewer, s.handleProjectMetrics))

	mux.HandleFunc("GET /projects/{id}/environments", rr(auth.RoleViewer, s.handleListEnvironments))
	mux.HandleFunc("POST /projects/{id}/environments", rr(auth.RoleAdmin, s.handleCreateEnvironment))
	mux.HandleFunc("PATCH /projects/{id}/environments/{envId}", rr(auth.RoleAdmin, s.handleUpdateEnvironment))
	mux.HandleFunc("DELETE /projects/{id}/environments/{envId}", rr(auth.RoleAdmin, s.handleDeleteEnvironment))
	mux.HandleFunc("GET /env-signals", rr(auth.RoleViewer, s.handleEnvSignals))
	mux.HandleFunc("GET /projects/{id}/environments/{envId}/mappings", rr(auth.RoleViewer, s.handleListEnvMappings))
	mux.HandleFunc("POST /projects/{id}/environments/{envId}/mappings", rr(auth.RoleDeveloper, s.handleProposeEnvMappings))
	mux.HandleFunc("POST /projects/{id}/environments/{envId}/mappings/approve", rr(auth.RoleAdmin, s.handleApproveEnvMappings))
	mux.HandleFunc("DELETE /projects/{id}/environments/{envId}/mappings/{mapId}", rr(auth.RoleAdmin, s.handleDeleteEnvMapping))
	mux.HandleFunc("POST /projects/{id}/environments/{envId}/collect", rr(auth.RoleDeveloper, s.handleCollectEnvNow))
	mux.HandleFunc("GET /projects/{id}/environments/{envId}/series", rr(auth.RoleViewer, s.handleEnvSeries))

	s.registerObservabilityRoutes(mux, rr)

	mux.HandleFunc("GET /projects/{id}/environments/{envId}/ingest-tokens", rr(auth.RoleAdmin, s.handleListIngestTokens))
	mux.HandleFunc("POST /projects/{id}/environments/{envId}/ingest-tokens", rr(auth.RoleAdmin, s.handleCreateIngestToken))
	mux.HandleFunc("PATCH /projects/{id}/environments/{envId}/ingest-tokens/{tokenId}", rr(auth.RoleAdmin, s.handleUpdateIngestToken))
	mux.HandleFunc("DELETE /projects/{id}/environments/{envId}/ingest-tokens/{tokenId}", rr(auth.RoleAdmin, s.handleRevokeIngestToken))
	mux.HandleFunc("GET /projects/{id}/environments/{envId}/ingest-sources", rr(auth.RoleViewer, s.handleListIngestSources))
	// Outside the role wrapper (own credential, listed in isPublic). Bound at request time because
	// SetIngest runs after route registration.
	for _, kind := range ingest.Kinds {
		mux.HandleFunc("POST /ingest/v1/"+kind, s.ingestRoute(kind))
	}

	mux.HandleFunc("GET /projects/{id}/stage-files", rr(auth.RoleViewer, s.handleListProjectStageFiles))
	mux.HandleFunc("POST /projects/{id}/stage-files/prune", rr(auth.RoleAdmin, s.handlePruneProjectStageFiles))
	mux.HandleFunc("GET /projects/{id}/stage-files/{kind}/{stage}", rr(auth.RoleViewer, s.handleReadProjectStageFile))
	mux.HandleFunc("PUT /projects/{id}/stage-files/{kind}/{stage}", rr(auth.RoleAdmin, s.handleWriteProjectStageFile))
	mux.HandleFunc("DELETE /projects/{id}/stage-files/{kind}/{stage}", rr(auth.RoleAdmin, s.handleDeleteProjectStageFile))

	mux.HandleFunc("GET /projects/{id}/init-script", rr(auth.RoleViewer, s.handleGetProjectInitScript))
	mux.HandleFunc("PUT /projects/{id}/init-script", rr(auth.RoleAdmin, s.handlePutProjectInitScript))
	mux.HandleFunc("GET /projects/{id}/init-script/revisions", rr(auth.RoleViewer, s.handleListProjectInitScriptRevisions))
	mux.HandleFunc("GET /projects/{id}/init-script/revisions/{rev}", rr(auth.RoleViewer, s.handleGetProjectInitScriptRevision))
	mux.HandleFunc("POST /projects/{id}/init-script/revisions/{rev}/restore", rr(auth.RoleAdmin, s.handleRestoreProjectInitScript))
	mux.HandleFunc("GET /projects/{id}/init-script/prewarm", rr(auth.RoleViewer, s.handleGetProjectPrewarm))
	mux.HandleFunc("GET /projects/{id}/init-script/repo-files", rr(auth.RoleViewer, s.handleProjectRootFiles))
	mux.HandleFunc("POST /projects/{id}/init-script/prewarm", rr(auth.RoleAdmin, s.handlePostProjectPrewarm))

	mux.HandleFunc("GET /projects/{id}/github-credential", rr(auth.RoleViewer, s.handleGetProjectGithubCredential))
	mux.HandleFunc("PUT /projects/{id}/github-credential", rr(auth.RoleAdmin, s.handleSetProjectGithubCredential))

	s.registerGithubAppRoutes(mux, rr)
	// The role gate is required: the env-key gate passes everything when OCTOPUS_API_KEY is unset.
	// Handlers also enforce requireTaskScope.
	mux.HandleFunc("GET /internal/projects/{id}/github-token", rr(auth.RoleDeveloper, s.handleInternalProjectGithubToken))
	mux.HandleFunc("GET /internal/github-token", rr(auth.RoleDeveloper, s.handleInternalRepoGithubToken))
	mux.HandleFunc("POST /internal/github/{owner}/{repo}/pulls/{number}/merge", rr(auth.RoleDeveloper, s.handleHeldPullMerge))
	mux.HandleFunc("POST /internal/github/{owner}/{repo}/runs/{runId}/deployment-review", rr(auth.RoleDeveloper, s.handleHeldDeploymentReview))
	mux.HandleFunc("GET /internal/environments/{envId}", rr(auth.RoleDeveloper, s.handleInternalEnvironment))
	mux.HandleFunc("GET /internal/credentials/available", rr(auth.RoleDeveloper, s.handleInternalAvailableCredentials))
	mux.HandleFunc("GET /internal/credentials/resolve", rr(auth.RoleDeveloper, s.handleInternalResolveCredential))
	mux.HandleFunc("GET /internal/tasks/{id}/github-token", rr(auth.RoleDeveloper, s.handleInternalTaskGithubToken))
	mux.HandleFunc("POST /internal/token-optimizer/delegate", rr(auth.RoleDeveloper, s.handleTokenOptimizerDelegate))
	mux.HandleFunc("POST /internal/token-optimizer/prepare", rr(auth.RoleDeveloper, s.handleTokenOptimizerPrepare))
	mux.HandleFunc("POST /internal/token-optimizer/finish", rr(auth.RoleDeveloper, s.handleTokenOptimizerFinish))
	mux.HandleFunc("POST /internal/tasks/{id}/branch-push", rr(auth.RoleDeveloper,
		gated(s.projectStore != nil, "api.server.errors.project_store_unavailable", s.handleInternalTaskBranchPush)))
	mux.HandleFunc("GET /internal/tasks/{id}/branch-status", rr(auth.RoleDeveloper,
		gated(s.projectStore != nil, "api.server.errors.project_store_unavailable", s.handleInternalTaskBranchStatus)))
	mux.HandleFunc("POST /internal/tasks/{id}/commit", rr(auth.RoleDeveloper,
		gated(s.projectStore != nil, "api.server.errors.project_store_unavailable", s.handleInternalTaskCommit)))
	mux.HandleFunc("POST /internal/tasks/{id}/pr", rr(auth.RoleDeveloper,
		gated(s.taskDispatcher != nil, "api.server.errors.dispatcher_unavailable", s.handleInternalTaskPR)))
}

func (s *Server) registerObservabilityRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /chart-types", rr(auth.RoleViewer, s.handleChartTypes))
	mux.HandleFunc("GET /projects/{id}/environments/{envId}/custom-metrics", rr(auth.RoleViewer, s.handleListCustomMetrics))
	mux.HandleFunc("PATCH /projects/{id}/environments/{envId}/custom-metrics/{metricId}", rr(auth.RoleAdmin, s.handleUpdateCustomMetric))
	mux.HandleFunc("DELETE /projects/{id}/environments/{envId}/custom-metrics/{metricId}", rr(auth.RoleAdmin, s.handleDeleteCustomMetric))
	mux.HandleFunc("GET /projects/{id}/environments/{envId}/custom-metrics/{metricId}/series", rr(auth.RoleViewer, s.handleCustomSeries))

	mux.HandleFunc("GET /projects/{id}/environments/{envId}/dashboards", rr(auth.RoleViewer, s.handleListDashboards))
	mux.HandleFunc("POST /projects/{id}/environments/{envId}/dashboards", rr(auth.RoleDeveloper, s.handleProposeDashboard))
	mux.HandleFunc("POST /tasks/{id}/environments/{envId}/mapping-proposals", rr(auth.RoleDeveloper, s.handleProposeTaskEnvMappings))
	mux.HandleFunc("POST /tasks/{id}/environments/{envId}/dashboard-proposals", rr(auth.RoleDeveloper, s.handleProposeTaskDashboard))
	mux.HandleFunc("GET /projects/{id}/environments/{envId}/dashboards/{dashId}", rr(auth.RoleViewer, s.handleGetDashboard))
	mux.HandleFunc("PUT /projects/{id}/environments/{envId}/dashboards/{dashId}", rr(auth.RoleAdmin, s.handleUpdateDashboard))
	mux.HandleFunc("POST /projects/{id}/environments/{envId}/dashboards/{dashId}/approve", rr(auth.RoleAdmin, s.handleApproveDashboard))
	mux.HandleFunc("DELETE /projects/{id}/environments/{envId}/dashboards/{dashId}", rr(auth.RoleAdmin, s.handleDeleteDashboard))
	mux.HandleFunc("GET /projects/{id}/environments/{envId}/dashboards/{dashId}/data", rr(auth.RoleViewer, s.handleDashboardData))
}

func (s *Server) registerQuestionRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("POST /tasks/{id}/questions", rr(auth.RoleDeveloper, s.handleAskQuestion))
	mux.HandleFunc("POST /tasks/{id}/questions/{qid}/answer", rr(auth.RoleDeveloper, s.handleAnswerQuestion))
	mux.HandleFunc("POST /tasks/{id}/questions/{qid}/cancel", rr(auth.RoleDeveloper, s.handleCancelQuestion))
	mux.HandleFunc("GET /inbox", rr(auth.RoleViewer, s.handleInbox))
}

func (s *Server) registerApprovalRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /tasks/{id}/approvals", rr(auth.RoleViewer, s.handleListApprovals))
	mux.HandleFunc("POST /tasks/{id}/approvals", rr(auth.RoleDeveloper, s.handleRequestApproval))
	mux.HandleFunc("POST /tasks/{id}/approvals/{aid}/decide", rr(auth.RoleDeveloper, s.handleDecideApproval))
	mux.HandleFunc("DELETE /tasks/{id}/approvals/{aid}", rr(auth.RoleDeveloper, s.handleDeleteApproval))
}

func (s *Server) registerTaskContainerRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /sandboxes", rr(auth.RoleViewer,
		gatedOK(s.sandbox.store != nil, []sandboxView{}, s.handleListSandboxes)))
	mux.HandleFunc("GET /sandboxes/queue", rr(auth.RoleViewer,
		gatedOK(s.sandbox.store != nil, []queueEntryView{}, s.handleListSandboxQueue)))
	mux.HandleFunc("GET /tasks/{id}/sandbox", rr(auth.RoleViewer, s.handleGetTaskSandbox))
	mux.HandleFunc("DELETE /tasks/{id}/sandbox", rr(auth.RoleAdmin, s.handleDeleteTaskSandbox))
	mux.HandleFunc("DELETE /tasks/{id}/sandbox/queue", rr(auth.RoleDeveloper,
		gated(s.sandbox.store != nil && s.sandbox.mgr != nil, "api.sandbox.errors.manager_unavailable", s.handleRemoveFromSandboxQueue)))
	mux.HandleFunc("DELETE /tasks/{id}/orphan-agents/{containerId}", rr(auth.RoleDeveloper, s.handleRemoveOrphanAgent))
}

func (s *Server) registerTaskRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /tasks", rr(auth.RoleViewer, s.handleListTasks))
	mux.HandleFunc("POST /tasks", rr(auth.RoleDeveloper, s.handleCreateTask))
	mux.HandleFunc("GET /tasks/{id}", rr(auth.RoleViewer, s.handleGetTask))
	mux.HandleFunc("PATCH /tasks/{id}", rr(auth.RoleDeveloper, s.handleUpdateTask))
	mux.HandleFunc("DELETE /tasks/{id}", rr(auth.RoleAdmin, s.handleDeleteTask))
	mux.HandleFunc("POST /tasks/{id}/subtasks", rr(auth.RoleDeveloper, s.handleCreateSubtask))
	mux.HandleFunc("PUT /tasks/{id}/subtasks/order", rr(auth.RoleDeveloper, s.handleReorderSubtasks))
	mux.HandleFunc("POST /tasks/{id}/clone", rr(auth.RoleDeveloper, s.handleCloneTask))
	mux.HandleFunc("POST /tasks/{id}/fork", rr(auth.RoleDeveloper, s.handleForkTask))
	mux.HandleFunc("POST /tasks/{id}/relaunch", rr(auth.RoleDeveloper, s.handleRelaunchTask))
	mux.HandleFunc("POST /tasks/{id}/queue/cancel", rr(auth.RoleDeveloper, s.handleCancelQueuedRun))
	mux.HandleFunc("POST /tasks/{id}/ensure-pr", rr(auth.RoleDeveloper, s.handleEnsurePR))
	mux.HandleFunc("POST /tasks/{id}/merge", rr(auth.RoleDeveloper, s.handleTaskMerge))
	mux.HandleFunc("GET /tasks/{id}/pull-requests", rr(auth.RoleViewer, s.handleTaskPullRequests))
	mux.HandleFunc("POST /tasks/{id}/comments", rr(auth.RoleDeveloper, s.handleAddComment))
	mux.HandleFunc("POST /tasks/{id}/threads/{threadId}/apply", rr(auth.RoleDeveloper, s.handleApplyThread))
	mux.HandleFunc("POST /tasks/{id}/threads/{threadId}/resolve", rr(auth.RoleDeveloper, s.handleResolveThread))
	mux.HandleFunc("POST /tasks/{id}/converse", rr(auth.RoleDeveloper, s.handleConverse))
	mux.HandleFunc("GET /tasks/{id}/conversation", rr(auth.RoleViewer, s.handleConversation))
	mux.HandleFunc("POST /tasks/{id}/conversation/{msg}/to-comment", rr(auth.RoleDeveloper, s.handleMessageToComment))
	mux.HandleFunc("PUT /tasks/{id}/session-snapshot", rr(auth.RoleDeveloper, s.handlePutSessionSnapshot))
	mux.HandleFunc("GET /tasks/{id}/session-snapshot", rr(auth.RoleDeveloper, s.handleGetSessionSnapshot))
	mux.HandleFunc("GET /tasks/{id}/conversation-transcript", rr(auth.RoleViewer, s.handleConversationTranscript))
	mux.HandleFunc("GET /tasks/{id}/attachments", rr(auth.RoleViewer, s.handleListAttachments))
	mux.HandleFunc("POST /tasks/{id}/attachments", rr(auth.RoleDeveloper, s.handleUploadAttachment))
	mux.HandleFunc("GET /tasks/{id}/brief", rr(auth.RoleViewer, s.handleGetTaskBrief))
	// Takes no id on purpose: the run is identified by its task-scoped key, so a pod cannot fetch
	// another agent's bundle.
	mux.HandleFunc("GET /run/bundle", rr(auth.RoleViewer, s.handleRunBundle))
	mux.HandleFunc("GET /tasks/{id}/reports", rr(auth.RoleViewer, s.handleListReports))
	mux.HandleFunc("POST /tasks/{id}/reports", rr(auth.RoleDeveloper, s.handleAddReport))
	mux.HandleFunc("GET /tasks/{id}/flags", rr(auth.RoleViewer, s.handleListFlags))
	mux.HandleFunc("POST /tasks/{id}/flags", rr(auth.RoleDeveloper, s.handleCreateFlag))
	mux.HandleFunc("POST /tasks/{id}/flags/import", rr(auth.RoleDeveloper, s.handleImportFlags))
	mux.HandleFunc("POST /tasks/{id}/auditor-feedback", rr(auth.RoleDeveloper, s.handleImportAuditorFeedback))
	mux.HandleFunc("PATCH /tasks/{id}/flags/{flagId}", rr(auth.RoleDeveloper, s.handleUpdateFlag))
	mux.HandleFunc("DELETE /tasks/{id}/flags/{flagId}", rr(auth.RoleDeveloper, s.handleDeleteFlag))
	mux.HandleFunc("GET /tasks/{id}/events", rr(auth.RoleViewer, s.handleListEvents))
	mux.HandleFunc("GET /tasks/{id}/sections", rr(auth.RoleViewer, s.handleListSections))
	mux.HandleFunc("PUT /tasks/{id}/sections/{slug}", rr(auth.RoleDeveloper, s.handleSetSection))
	mux.HandleFunc("DELETE /tasks/{id}/sections/{slug}", rr(auth.RoleDeveloper, s.handleDeleteSection))
	mux.HandleFunc("PATCH /tasks/{id}/sections/{slug}/items/{index}", rr(auth.RoleDeveloper, s.handleCheckSectionItem))
	s.registerTestCaseRoutes(mux, rr)
	mux.HandleFunc("GET /tasks/{id}/usage", rr(auth.RoleViewer, s.handleTaskUsage))
	mux.HandleFunc("GET /tasks/{id}/dependencies", rr(auth.RoleViewer, s.handleListTaskDependencies))
	mux.HandleFunc("POST /tasks/{id}/dependencies", rr(auth.RoleDeveloper, s.handleAddTaskDependency))
	mux.HandleFunc("DELETE /tasks/{id}/dependencies/{depId}", rr(auth.RoleDeveloper, s.handleRemoveTaskDependency))
	mux.HandleFunc("GET /task-attachments/{attachmentId}", rr(auth.RoleViewer, s.handleServeAttachment))
	mux.HandleFunc("DELETE /task-attachments/{attachmentId}", rr(auth.RoleDeveloper, s.handleDeleteAttachment))
	s.registerEvidenceRoutes(mux, rr)
	s.registerRunnerRoutes(mux, rr)
	s.registerTaskContainerRoutes(mux, rr)

	// Admin only: the list exposes each server's SSE URL, a map of the operator's private network.
	mux.HandleFunc("GET /mcps", rr(auth.RoleAdmin, s.handleListMCPs))
	mux.HandleFunc("POST /mcps/register", rr(auth.RoleAdmin, s.handleRegisterMCP))
	mux.HandleFunc("PATCH /mcps/{name}", rr(auth.RoleAdmin, s.handleUpdateMCP))
	mux.HandleFunc("DELETE /mcps/{name}", rr(auth.RoleAdmin, s.handleDeleteMCP))
	mux.HandleFunc("POST /mcps/{name}/start", rr(auth.RoleAdmin,
		gated(s.mcp.mgr != nil, "api.server.errors.mcp_manager_unavailable", s.handleStartMCP)))
	mux.HandleFunc("POST /mcps/{name}/stop", rr(auth.RoleAdmin,
		gated(s.mcp.mgr != nil, "api.server.errors.mcp_manager_unavailable", s.handleStopMCP)))
	mux.HandleFunc("POST /mcps/{name}/restart", rr(auth.RoleAdmin,
		gated(s.mcp.mgr != nil, "api.server.errors.mcp_manager_unavailable", s.handleRestartMCP)))
}

func (s *Server) registerMCPProxyRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("POST /mcps/probe", rr(auth.RoleAdmin, s.handleProbeMCP))
	mux.HandleFunc("GET /mcp-proxy/{name}", rr(auth.RoleDeveloper, s.handleMCPProxy))
	mux.HandleFunc("POST /mcp-proxy/{name}", rr(auth.RoleDeveloper, s.handleMCPProxy))
	mux.HandleFunc("DELETE /mcp-proxy/{name}", rr(auth.RoleDeveloper, s.handleMCPProxy))
	mux.HandleFunc("POST /mcp-proxy/{name}/messages", rr(auth.RoleDeveloper, s.handleMCPProxyMessages))
}

func (s *Server) registerTestCaseRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /tasks/{id}/test-cases", rr(auth.RoleViewer, s.handleListTestCases))
	mux.HandleFunc("POST /tasks/{id}/test-cases", rr(auth.RoleDeveloper, s.handleAddTestCases))
	mux.HandleFunc("POST /tasks/{id}/test-cases/import-body", rr(auth.RoleDeveloper, s.handleImportTestCasesFromBody))
	mux.HandleFunc("PATCH /tasks/{id}/test-cases/{case}", rr(auth.RoleDeveloper, s.handleUpdateTestCase))
	mux.HandleFunc("DELETE /tasks/{id}/test-cases/{case}", rr(auth.RoleDeveloper, s.handleDeleteTestCase))
	mux.HandleFunc("POST /tasks/{id}/test-cases/{case}/runs", rr(auth.RoleDeveloper, s.handleRecordTestCaseRun))
	mux.HandleFunc("GET /test-cases", rr(auth.RoleViewer, s.handleListWorkspaceTestCases))
}

func (s *Server) registerAutomationRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /automations", rr(auth.RoleDeveloper, s.handleListAutomations))
	mux.HandleFunc("POST /automations", rr(auth.RoleAdmin, s.handleCreateAutomation))
	// Admin even though it stores nothing: it is a POST carrying flows:write, so a lower gate puts
	// that permission in a developer key's reach.
	mux.HandleFunc("POST /automations/validate", rr(auth.RoleAdmin, s.handleValidateAutomation))
	mux.HandleFunc("GET /automations/{id}", rr(auth.RoleDeveloper, s.handleGetAutomation))
	mux.HandleFunc("PUT /automations/{id}", rr(auth.RoleAdmin, s.handleUpdateAutomation))
	mux.HandleFunc("DELETE /automations/{id}", rr(auth.RoleAdmin, s.handleDeleteAutomation))
	mux.HandleFunc("PUT /automations/{id}/enabled", rr(auth.RoleAdmin, s.handleSetAutomationEnabled))
	mux.HandleFunc("POST /automations/{id}/run", rr(auth.RoleAdmin, s.handleRunAutomation))
	mux.HandleFunc("GET /automations/{id}/runs", rr(auth.RoleDeveloper, s.handleListAutomationRuns))
	mux.HandleFunc("GET /automations/{id}/runs/{runId}", rr(auth.RoleDeveloper, s.handleGetAutomationRun))
	mux.HandleFunc("POST /automations/{id}/runs/{runId}/cancel", rr(auth.RoleAdmin, s.handleCancelAutomationRun))
}

func (s *Server) registerUpdateRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /updates/status", rr(auth.RoleAdmin, s.updatesOperator(s.handleUpdatesStatus)))
	mux.HandleFunc("POST /updates/check", rr(auth.RoleAdmin, s.updatesOperator(s.handleUpdatesCheck)))
	mux.HandleFunc("GET /updates/package", rr(auth.RoleAdmin, s.updatesOperator(s.handleUpdatesPackage)))
	mux.HandleFunc("GET /updates/versions", rr(auth.RoleAdmin, s.updatesOperator(s.handleUpdatesVersions)))
	mux.HandleFunc("GET /updates/history", rr(auth.RoleAdmin, s.updatesOperator(s.handleUpdatesHistory)))
	mux.HandleFunc("POST /updates/events", rr(auth.RoleAdmin, s.updatesOperator(s.handleUpdatesEvent)))
	mux.HandleFunc("GET /updates/backups", rr(auth.RoleAdmin, s.updatesOperator(s.handleBackups)))
	mux.HandleFunc("/updates", rr(auth.RoleViewer, s.handleUpdatesNotFound))
	mux.HandleFunc("/updates/", rr(auth.RoleViewer, s.handleUpdatesNotFound))
}

func (s *Server) registerOpsRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /settings", rr(auth.RoleAdmin,
		gatedOK(s.settingsStore != nil, map[string]any{}, s.handleListSettings)))
	mux.HandleFunc("GET /settings/{key}", rr(auth.RoleAdmin,
		gated(s.settingsStore != nil, "api.server.errors.settings_store_unavailable", s.handleGetSetting)))
	mux.HandleFunc("PUT /settings/{key}", rr(auth.RoleAdmin,
		gated(s.settingsStore != nil, "api.server.errors.settings_store_unavailable", s.handlePutSetting)))
	mux.HandleFunc("PATCH /settings", rr(auth.RoleAdmin,
		gated(s.settingsStore != nil, "api.server.errors.settings_store_unavailable", s.handlePatchSettings)))
	mux.HandleFunc("GET /workspace-settings", rr(auth.RoleViewer, s.gatedSettings(s.handleGetWorkspaceSettings)))
	mux.HandleFunc("PUT /workspace-settings/{key}", rr(auth.RoleAdmin, s.gatedSettings(s.handlePutWorkspaceSetting)))

	mux.HandleFunc("GET /webhooks", rr(auth.RoleAdmin, s.handleListWebhooks))
	mux.HandleFunc("GET /webhooks/events", rr(auth.RoleAdmin, s.handleListWebhookEvents))
	mux.HandleFunc("POST /webhooks", rr(auth.RoleAdmin, s.handleCreateWebhook))
	mux.HandleFunc("POST /webhooks/{id}/test", rr(auth.RoleAdmin, s.handleTestWebhook))
	mux.HandleFunc("POST /webhooks/{id}/rotate-secret", rr(auth.RoleAdmin, s.handleRotateWebhookSecret))
	mux.HandleFunc("GET /webhooks/{id}/deliveries", rr(auth.RoleAdmin, s.handleListWebhookDeliveries))
	mux.HandleFunc("POST /webhooks/{id}/deliveries/{deliveryId}/retry", rr(auth.RoleAdmin, s.handleRetryWebhookDelivery))
	mux.HandleFunc("DELETE /webhooks/{id}", rr(auth.RoleAdmin, s.handleDeleteWebhook))

	s.registerAutomationRoutes(mux, rr)

	s.registerUpdateRoutes(mux, rr)

	s.registerInfraRoutes(mux, rr)

	mux.HandleFunc("GET /audit", rr(auth.RoleAdmin, s.handleListAudit))
	mux.HandleFunc("GET /audit/export", rr(auth.RoleAdmin, s.handleExportAudit))
	mux.HandleFunc("GET /audit/verify", rr(auth.RoleAdmin, s.handleVerifyAudit))

	mux.HandleFunc("GET /engines", rr(auth.RoleAdmin, s.handleListEngines))
	mux.HandleFunc("GET /llm-key", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleGetLLMKey)))
	mux.HandleFunc("PUT /llm-key", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleSetLLMKey)))
	mux.HandleFunc("DELETE /llm-key", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleDeleteLLMKey)))

	mux.HandleFunc("GET /llm-profiles", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleListLLMProfiles)))
	mux.HandleFunc("POST /llm-profiles", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleCreateLLMProfile)))
	mux.HandleFunc("GET /llm-profiles/{id}", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleGetLLMProfile)))
	mux.HandleFunc("PATCH /llm-profiles/{id}", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleUpdateLLMProfile)))
	mux.HandleFunc("DELETE /llm-profiles/{id}", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleDeleteLLMProfile)))
	mux.HandleFunc("POST /llm-profiles/{id}/check", rr(auth.RoleAdmin,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleCheckLLMProfile)))
	mux.HandleFunc("GET /workload-identity", rr(auth.RoleAdmin,
		gated(s.workloadIdentity != nil, "api.server.errors.secrets_store_unavailable", s.handleGetWorkloadIdentity)))
	mux.HandleFunc("POST /workload-identity/rotate", rr(auth.RoleAdmin,
		gated(s.workloadIdentity != nil, "api.server.errors.secrets_store_unavailable", s.handleRotateWorkloadIdentity)))

	mux.HandleFunc("GET /agents/{name}/llm-binding", rr(auth.RoleViewer,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleGetAgentLLMBinding)))
	mux.HandleFunc("PUT /agents/{name}/llm-binding", rr(auth.RoleDeveloper,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handlePutAgentLLMBinding)))
	mux.HandleFunc("DELETE /agents/{name}/llm-binding", rr(auth.RoleDeveloper,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleDeleteAgentLLMBinding)))
	mux.HandleFunc("GET /agents/{name}/effective-llm", rr(auth.RoleViewer,
		gated(s.secretsStore != nil, "api.server.errors.secrets_store_unavailable", s.handleGetEffectiveLLM)))

	mux.HandleFunc("GET /credential-providers", rr(auth.RoleAdmin, s.handleListCredentialProviders))
	mux.HandleFunc("GET /credentials", rr(auth.RoleAdmin, s.handleListCredentials))
	mux.HandleFunc("POST /credentials", rr(auth.RoleAdmin, s.handleCreateCredential))
	mux.HandleFunc("GET /credentials/decisions", rr(auth.RoleAdmin, s.handleListCredentialDecisions))
	mux.HandleFunc("GET /credentials/{id}", rr(auth.RoleAdmin, s.handleGetCredential))
	mux.HandleFunc("PATCH /credentials/{id}", rr(auth.RoleAdmin, s.handleUpdateCredential))
	mux.HandleFunc("DELETE /credentials/{id}", rr(auth.RoleAdmin, s.handleDeleteCredential))
	mux.HandleFunc("POST /credentials/{id}/validate", rr(auth.RoleAdmin, s.handleValidateCredential))
	mux.HandleFunc("PUT /credentials/{id}/disabled", rr(auth.RoleAdmin, s.handleSetCredentialDisabled))
	mux.HandleFunc("PUT /credentials/{id}/grants", rr(auth.RoleAdmin, s.handleSetCredentialGrants))
	mux.HandleFunc("PUT /credentials/{id}/tools", rr(auth.RoleAdmin, s.handleSetCredentialTools))
	// Callback and client document are reached without Thulu credentials; they must stay listed
	// in isPublic in buildHandler.
	mux.HandleFunc("GET /credentials/oauth/servers", rr(auth.RoleAdmin, s.handleListOAuthServers))
	mux.HandleFunc("POST /credentials/oauth/start", rr(auth.RoleAdmin, s.handleStartOAuth))
	mux.HandleFunc("POST /credentials/{id}/oauth/disconnect", rr(auth.RoleAdmin, s.handleDisconnectOAuth))
	mux.HandleFunc("GET "+mcpOAuthCallbackPath, s.handleOAuthCallback)
	mux.HandleFunc("GET "+mcpOAuthClientPath, s.handleOAuthClientMetadata)

	mux.HandleFunc("GET /onboarding", rr(auth.RoleAdmin, s.handleOnboarding))

	mux.HandleFunc("GET /entitlements", rr(auth.RoleViewer, s.handleGetEntitlements))
	mux.HandleFunc("PUT /entitlements", rr(auth.RoleAdmin, s.handleSetEntitlementPlan))
	mux.HandleFunc("PUT /entitlements/extra-hours-cap", rr(auth.RoleAdmin, s.handleSetExtraHoursCap))

	mux.HandleFunc("DELETE /workspaces/{id}", rr(auth.RoleAdmin,
		gated(s.authStore != nil, "api.server.errors.auth_disabled", s.handleDeleteWorkspace)))

	s.registerChatRoutes(mux, rr)

	s.registerSandboxMCPRoutes(mux, rr)
}

func (s *Server) registerChatRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("POST /chats", rr(auth.RoleDeveloper, s.handleCreateChat))
	mux.HandleFunc("GET /chats", rr(auth.RoleViewer, s.handleListChats))
	mux.HandleFunc("GET /chats/agents", rr(auth.RoleViewer, s.handleListChatAgents))
	mux.HandleFunc("GET /events", rr(auth.RoleViewer,
		gated(s.hub != nil, "api.server.errors.event_hub_unavailable", s.handleEventsStream)))
	mux.HandleFunc("POST /events/{id}/subscribe", rr(auth.RoleViewer,
		gated(s.hub != nil, "api.server.errors.event_hub_unavailable", s.handleEventsSubscribe)))
	mux.HandleFunc("POST /events/{id}/unsubscribe", rr(auth.RoleViewer,
		gated(s.hub != nil, "api.server.errors.event_hub_unavailable", s.handleEventsUnsubscribe)))

	mux.HandleFunc("GET /chats/events", rr(auth.RoleViewer, s.handleChatEvents))
	mux.HandleFunc("GET /chats/{id}", rr(auth.RoleViewer, s.handleGetChat))
	mux.HandleFunc("PATCH /chats/{id}", rr(auth.RoleDeveloper, s.handlePatchChat))
	mux.HandleFunc("DELETE /chats/{id}", rr(auth.RoleDeveloper, s.handleDeleteChat))
	mux.HandleFunc("POST /chats/{id}/reset", rr(auth.RoleDeveloper, s.handleResetChat))
	mux.HandleFunc("GET /chats/{id}/messages", rr(auth.RoleViewer, s.handleListChatMessages))
	mux.HandleFunc("POST /chats/{id}/messages", rr(auth.RoleDeveloper, s.handlePostChatMessage))
	mux.HandleFunc("POST /chats/{id}/delegation", rr(auth.RoleDeveloper, s.handleChatDelegation))
	mux.HandleFunc("GET /chats/{id}/actions", rr(auth.RoleViewer, s.handleListChatActions))
	mux.HandleFunc("POST /chats/{id}/actions/{actionId}/confirm", rr(auth.RoleDeveloper, s.handleConfirmChatAction))
	mux.HandleFunc("POST /chats/{id}/actions/{actionId}/cancel", rr(auth.RoleDeveloper, s.handleCancelChatAction))
	mux.HandleFunc("GET /chat-delegation", rr(auth.RoleViewer, s.handleGetDelegationPolicy))
	mux.HandleFunc("PUT /chat-delegation", rr(auth.RoleOwner, s.handlePutDelegationPolicy))
	// Preferences belong to a person, and people live in the auth store.
	hasPeople := s.authStore != nil
	mux.HandleFunc("GET /me/preferences", rr(auth.RoleViewer, gated(hasPeople, "api.server.errors.auth_disabled", s.handleGetPreferences)))
	mux.HandleFunc("PUT /me/preferences/{key}", rr(auth.RoleViewer, gated(hasPeople, "api.server.errors.auth_disabled", s.handlePutPreference)))
	mux.HandleFunc("GET /me/engine-logins", rr(auth.RoleViewer, s.handleListEngineLogins))
	mux.HandleFunc("POST /me/engine-logins/{engine}", rr(auth.RoleDeveloper, s.handleStartEngineLogin))
	mux.HandleFunc("GET /me/engine-logins/{engine}/flow", rr(auth.RoleDeveloper, s.handleEngineLoginFlow))
	mux.HandleFunc("POST /me/engine-logins/{engine}/code", rr(auth.RoleDeveloper, s.handleCompleteEngineLogin))
	mux.HandleFunc("POST /me/engine-logins/{engine}/cancel", rr(auth.RoleDeveloper, s.handleCancelEngineLogin))
	mux.HandleFunc("DELETE /me/engine-logins/{engine}", rr(auth.RoleDeveloper, s.handleDisconnectEngineLogin))
}

// The exec channel runs arbitrary commands in task containers: never anonymous. The role gate is
// only the floor; sandboxAuthorizer decides which task per session.
func (s *Server) registerSandboxMCPRoutes(mux *http.ServeMux, rr roleGate) {
	if s.sandbox.mcp == nil {
		return
	}
	mux.HandleFunc("GET /sandbox/sse", rr(auth.RoleDeveloper, s.sandbox.mcp.HandleSSE))
	mux.HandleFunc("POST /sandbox/messages", rr(auth.RoleDeveloper, s.sandbox.mcp.HandleMessages))
}

// The series are install-wide (every tenant's traffic and spend): only the OCTOPUS_METRICS_TOKEN
// holder or an operator-workspace admin may read them.
func (s *Server) gateMetrics(next http.Handler) http.HandlerFunc {
	operatorOnly := func(w http.ResponseWriter, r *http.Request) {
		if !s.requireOperator(w, r, "api.scope.operator_surfaces.metrics", "api.scope.operator_verbs.read") {
			return
		}
		next.ServeHTTP(w, r)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(os.Getenv("OCTOPUS_METRICS_TOKEN"))
		if token == "" {
			auth.RequireRole(auth.RoleAdmin, operatorOnly)(w, r)
			return
		}
		//i18n:ignore HTTP auth scheme, not a message
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) != 1 {
			writeErrKey(w, r, http.StatusUnauthorized, "api.server.errors.metrics_token_required")
			return
		}
		next.ServeHTTP(w, r)
	}
}

// Must admit the same callers as gateMetrics.
func (s *Server) healthDetailAllowed(r *http.Request) bool {
	if token := strings.TrimSpace(os.Getenv("OCTOPUS_METRICS_TOKEN")); token != "" {
		//i18n:ignore HTTP auth scheme, not a message
		if got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok &&
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) == 1 {
			return true
		}
	}
	return s.callerIsOperator(r)
}

func isUnboundedRoute(r *http.Request) bool {
	p := r.URL.Path
	switch p {
	case "/events",
		"/chats/events",
		"/sandbox/sse",
		"/audit/export":
		return true
	}
	if strings.HasPrefix(p, "/git/") || strings.HasPrefix(p, "/mcp-proxy/") {
		return true
	}
	if isRunnerLongCall(r) {
		return true
	}
	if r.Method == http.MethodPost && p == "/projects" {
		return true
	}
	if strings.HasPrefix(p, "/projects/") &&
		((r.Method == http.MethodPost && strings.HasSuffix(p, "/upload")) ||
			(r.Method == http.MethodGet && strings.HasSuffix(p, "/archive"))) {
		return true
	}
	if isFileTransfer(r.Method, p) {
		return true
	}
	if strings.HasSuffix(p, "/logs") || strings.HasSuffix(p, "/logs/stream") {
		return true
	}
	return false
}

func isFileTransfer(method, p string) bool {
	post := method == http.MethodPost
	return strings.HasPrefix(p, "/evidence/artifacts/") ||
		(post && strings.HasPrefix(p, "/evidence/jobs/") && strings.HasSuffix(p, "/artifacts")) ||
		strings.HasPrefix(p, "/task-attachments/") ||
		(post && strings.HasPrefix(p, "/tasks/") && strings.HasSuffix(p, "/attachments"))
}

func (s *Server) buildHandler(mux *http.ServeMux) http.Handler {
	rl := middleware.NewRateLimiter()
	var handler http.Handler = mux
	handler = i18n.Middleware(handler)
	handler = auth.CSRFMiddleware(handler)
	handler = middleware.RequestBodyLimit(handler)
	handler = middleware.SecurityHeaders(handler)
	handler = middleware.CORS(handler)
	// The env-key gate reads ClaimsFromContext, so JWTMiddleware must run first, i.e. be wrapped
	// after this.
	// SPA assets bypass the gate; overlapping API routes are still gated by RequireRole.
	var spaIsAsset func(string) bool
	if s.spa != nil {
		spaIsAsset = s.spa.IsAsset
	}
	// Every path here skips the env-key gate and must authenticate in its own handler (git
	// Basic-auth, HMAC/state token, OAuth state + binder cookie, ingest token).
	isPublic := func(p string) bool {
		return strings.HasPrefix(p, "/git/") ||
			strings.HasPrefix(p, "/ingest/") ||
			p == "/github/webhook" || p == "/github/setup" || p == "/github/oauth/callback" ||
			p == "/github/manifest/callback" ||
			p == workloadid.DiscoveryPath || p == workloadid.JWKSPath ||
			p == mcpOAuthCallbackPath || p == mcpOAuthClientPath ||
			isRunnerAPI(p) ||
			(spaIsAsset != nil && spaIsAsset(p))
	}
	handler = auth.MiddlewareWithStoreAndPublic(s.authStore, isPublic, handler)
	handler = auth.JWTMiddlewareWithStore(s.authStore, handler)
	// Must wrap outside the auth gates so they can read the dev session.
	handler = auth.DevSessionMiddleware(handler)
	handler = rl.Handler(handler)
	// Outside the rate limiter and auth so the deadline covers their DB reads; inside
	// RequestID/Logger so timeouts are logged with an id.
	handler = middleware.Timeout(middleware.RequestTimeout(), isUnboundedRoute, handler)
	handler = metrics.Middleware(handler)
	handler = middleware.RequestID(handler)
	handler = middleware.Logger(handler)
	handler = middleware.Recover(handler)
	return handler
}

func (s *Server) RegisterHealthDB(name string, db *sql.DB) {
	s.healthChecker.RegisterDB(name, db)
}

func (s *Server) Start() error {
	certFile, keyFile, err := tlsFilesFromEnv()
	if err != nil {
		return err
	}
	if certFile == "" {
		log.Printf("[api] %s", i18n.Log("api.server.log.listening", "addr", s.srv.Addr))
		return s.srv.ListenAndServe()
	}
	reloader, err := newCertReloader(certFile, keyFile)
	if err != nil {
		return err
	}
	s.srv.TLSConfig = serverTLSConfig(reloader)
	log.Printf("[api] %s", i18n.Log("api.server.log.listening_tls", "addr", s.srv.Addr))
	return s.srv.ListenAndServeTLS("", "")
}

func (s *Server) Dispatcher() *task.Dispatcher { return s.taskDispatcher }

// Call once at startup, after the watcher has synced existing containers into the registry.
func (s *Server) ReconcileOrphans(ctx context.Context) {
	s.taskDispatcher.Reconcile(ctx)
	s.taskDispatcher.ReconcileDone(ctx)
	// Before ReconcileQuestions: a queued resume must get its awaiting_input back.
	s.taskDispatcher.ResumeQueued(ctx)
	// After the replayed exits: they may have parked a task on its questions.
	s.taskDispatcher.ReconcileQuestions(ctx)
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.bgCancel != nil {
		s.bgCancel()
	}
	return s.srv.Shutdown(ctx)
}

// DefaultSourceURL is where the dashboard's "Source code" link points. AGPL §13 asks whoever runs a
// modified copy to offer that copy's source, so OCTOPUS_SOURCE_URL overrides it.
const DefaultSourceURL = "https://github.com/MisterDonnuts/thulu"

func sourceURL() string {
	if v := strings.TrimSpace(os.Getenv("OCTOPUS_SOURCE_URL")); v != "" {
		return v
	}
	return DefaultSourceURL
}

// Anonymous callers get the edition and the source link only: the version reveals which advisories
// apply and the licensee names the organization.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !auth.IsAPIKeyAuthed(ctx) && auth.ClaimsFromContext(ctx) == nil && !auth.DevSession(ctx) {
		writeJSON(w, http.StatusOK, map[string]any{"edition": edition.Current().Edition, "source_url": sourceURL()})
		return
	}
	lic := edition.Current()
	info := map[string]any{
		"edition":    lic.Edition,
		"version":    buildinfo.Version,
		"licensee":   lic.Licensee,
		"features":   lic.Features,
		"source_url": sourceURL(),
	}
	if exp := lic.ExpiresAt; !exp.IsZero() {
		info["license_expires_at"] = exp.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, info)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	apperr.WriteJSON(w, r, status, err)
}

func writeErrKey(w http.ResponseWriter, r *http.Request, status int, key string, args ...any) {
	apperr.WriteJSON(w, r, status, apperr.New(key, args...))
}

// present is computed by the caller: an interface holding a nil pointer is not == nil.
// Only for deps set in New; Set*-wired deps are still nil at registration and must be checked per
// request.
func gated(present bool, errKey string, h http.HandlerFunc) http.HandlerFunc {
	if present {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeErrKey(w, r, http.StatusServiceUnavailable, errKey)
	}
}

func gatedOK(present bool, body any, h http.HandlerFunc) http.HandlerFunc {
	if present {
		return h
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, body)
	}
}

func writeStatus(w http.ResponseWriter, verb string) {
	writeJSON(w, http.StatusOK, map[string]string{"status": verb})
}

// nil marshals to null and the dashboard calls .length on list fields.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body"))
		return false
	}
	return true
}

func decodeOptionalBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body"))
		return false
	}
	return true
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}
