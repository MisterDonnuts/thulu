package chatruntime

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kubert"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/syncx"
	"github.com/MisterDonnuts/thulu/internal/types"
)

var ErrTurnInFlight error = apperr.Conflict("chatruntime.errors.turn_in_flight")

var ErrNoCapacity error = apperr.Unavailable("chatruntime.errors.no_capacity")

const workspaceAtCapKey = "chatruntime.errors.workspace_at_cap"

func IsWorkspaceAtCap(err error) bool { return apperr.Code(err) == workspaceAtCapKey }

type TurnResult struct {
	Text        string
	InputTokens int64
	DurationMs  int64
	SessionID   string
	IsError     bool
	Tools       []chat.ToolCall
	Effects     []chat.Effect

	Model         string
	OutputTokens  int64
	CacheRead     int64
	CacheCreation int64
	// ContextTokens is what the last request of the turn carried: the conversation's real size.
	ContextTokens int64
	CostUSD       float64
	NumTurns      int
	StopReason    string
}

const maxToolHint = 120

const maxTurnTools = 50

type AcquireOpts struct {
	Code map[string]types.CodeView
	// TurnID names the turn's live feed and transcript (BeginLive); "" runs the turn unrecorded.
	TurnID string
}

type Manager struct {
	store    *Store
	settings *settings.Store
	backend  backend
	// Chats placed on a registered machine; their runtime ids say so (machineOf).
	machine backend

	dataDir string

	apiBaseURL string

	mcpFwd *mcp.Forwarder

	browser         browserrun.Config
	noPerRunBrowser bool

	llmAuth LLMAuthFunc

	augment AgentAugmentFunc

	workspaceLimit func(workspaceID string) int
	admit          func(ctx context.Context, workspaceID string) error

	keys RunKeyMinter

	// Re-reads the delegation under the chat's lifecycle lock, so a turn that loaded the chat
	// earlier cannot
	// mint a delegated key after EndDelegation revoked them.
	chats ChatReader

	chatMu syncx.KeyedMutex
	turns  sync.Map
	// cancels holds each running turn's cancel, for CancelTurn.
	cancels sync.Map
	live    liveTurns
	keyMu   sync.Mutex
	keyByID map[string]string

	// pending counts per-workspace containers admitted but not yet live, so a burst of acquires
	// cannot all
	// pass the cap check against the same free slot. Guarded by capMu.
	capMu   sync.Mutex
	pending map[string]int

	// orphanEvery spaces ReapOrphans; zero means orphanSweepInterval.
	orphanEvery time.Duration
}

// orphanSweepInterval spaces the sweeps for chat containers and pods no live row claims. Reconcile
// does the same at boot; this catches what a failed remove or a half-done teardown leaves later.
const orphanSweepInterval = 5 * time.Minute

type RunKeyMinter interface {
	MintRunKey(ctx context.Context, scope auth.RunScope, workspaceID string, ttl time.Duration) (rawKey, keyID string, err error)
	RevokeAPIKey(ctx context.Context, keyID string) error
	// Must also revoke keys minted before a daemon restart, which keyByID no longer knows.
	RevokeChatRunKeys(ctx context.Context, chatID string) error
}

type ChatReader interface {
	GetChat(ctx context.Context, id string) (*chat.Chat, error)
}

const runKeyEnv = "OCTOPUS_API_KEY"

// Bounds the key's life if the revoke on teardown is lost (daemon crash).
const chatKeyTTL = 24 * time.Hour

const defaultDockerAPIBase = "http://host.docker.internal:8585"

type ManagerConfig struct {
	DataDir        string
	DataVolume     string
	Kube           *kubert.Runtime
	GitBaseURL     string
	APIBaseURL     string
	WorkspaceLimit func(workspaceID string) int
	// Admit refuses a new chat container when the workspace has no hours left, its trial ended or its
	// plan is inactive. nil admits everything (self-hosted).
	Admit        func(ctx context.Context, workspaceID string) error
	MCPForwarder *mcp.Forwarder
	Browser      browserrun.Config
	Launch       launchFunc
}

func NewManager(store *Store, sett *settings.Store, docker *client.Client, cfg ManagerConfig) *Manager {
	m := &Manager{
		store:          store,
		keyByID:        map[string]string{},
		settings:       sett,
		dataDir:        cfg.DataDir,
		apiBaseURL:     cfg.APIBaseURL,
		mcpFwd:         cfg.MCPForwarder,
		workspaceLimit: cfg.WorkspaceLimit,
		admit:          cfg.Admit,
		machine:        unavailableBackend{},
	}
	switch {
	case docker != nil || cfg.Launch != nil:
		launch := cfg.Launch
		if launch == nil {
			launch = func(ctx context.Context, at types.AgentType, params types.LaunchParams) (string, error) {
				return types.Launch(ctx, docker, at, params)
			}
		}
		m.backend = &dockerBackend{
			docker:     docker,
			launch:     launch,
			dataDir:    cfg.DataDir,
			dataVolume: cfg.DataVolume,
		}
		if m.apiBaseURL == "" {
			m.apiBaseURL = defaultDockerAPIBase
		}
		// The Docker per-run browser is started by the task launcher (internal/api), which chat
		// containers bypass.
		m.noPerRunBrowser = cfg.Browser.PerRun()
	case cfg.Kube != nil:
		m.backend = newKubeBackend(cfg.Kube, cfg.GitBaseURL)
		m.browser = cfg.Browser
	default:
		m.backend = unavailableBackend{}
	}
	return m
}

func newManagerForTest(store *Store, sett *settings.Store, docker dockerAPI, cfg ManagerConfig) *Manager {
	m := NewManager(store, sett, nil, cfg)
	m.backend = &dockerBackend{
		docker:     docker,
		launch:     cfg.Launch,
		dataDir:    cfg.DataDir,
		dataVolume: cfg.DataVolume,
	}
	return m
}

func (m *Manager) TurnInFlight(chatID string) bool {
	_, ok := m.turns.Load(chatID)
	return ok
}

func (m *Manager) BeginTurn(chatID string) error {
	if _, loaded := m.turns.LoadOrStore(chatID, struct{}{}); loaded {
		return ErrTurnInFlight
	}
	return nil
}

func (m *Manager) EndTurn(chatID string) { m.turns.Delete(chatID) }

// ErrTurnCanceled is how a turn its person stopped fails.
var ErrTurnCanceled error = apperr.New("chatruntime.errors.turn_canceled")

// CancelTurn stops the chat's running turn; the backend kills the engine's processes as it does on
// a timeout. False when no turn is running.
func (m *Manager) CancelTurn(chatID string) bool {
	v, ok := m.cancels.Load(chatID)
	if !ok {
		return false
	}
	cancel, ok := v.(context.CancelCauseFunc)
	if !ok {
		return false
	}
	cancel(ErrTurnCanceled)
	return true
}

type LLMAuthFunc func(env map[string]string, workspaceID, agentName string, agentType *types.AgentType) error

func (m *Manager) SetLLMAuth(fn LLMAuthFunc) { m.llmAuth = fn }

// Must not modify at; returns at itself when nothing changes.
type AgentAugmentFunc func(ctx context.Context, chatID string, at *types.AgentType) *types.AgentType

func (m *Manager) SetAgentAugment(fn AgentAugmentFunc) { m.augment = fn }

func (m *Manager) SetRunKeys(k RunKeyMinter) { m.keys = k }

// SetMachineTurns lets chats run on the machine their placement names. Without it such a chat is
// refused by the unavailable backend.
func (m *Manager) SetMachineTurns(t MachineTurns) { m.machine = newRunnerBackend(t) }

func (m *Manager) backendFor(id string) backend {
	if machineOf(id) != "" {
		return m.machine
	}
	return m.backend
}

// chatAuth runs the chat's auth on what a turn is resolved from. It also places the chat, as it
// places a run: OCTOPUS_RUNNER_ID names the machine, absent for the container or pod.
func (m *Manager) chatAuth(c *chat.Chat, at *types.AgentType) (map[string]string, error) {
	env := turnEnv(c, at)
	if m.llmAuth == nil {
		return env, nil
	}
	if err := m.llmAuth(env, c.WorkspaceID, at.Name, at); err != nil {
		return nil, err
	}
	return env, nil
}

// A chat not linked to a task gets a read-only key (auth.runKeyPermissions), so a chat cannot move
// tasks
// unless delegated, and then only until the delegation ends.
func (m *Manager) applyRunKey(ctx context.Context, env map[string]string, c *chat.Chat, at *types.AgentType, delegatedUntil *time.Time) {
	if m.keys == nil {
		return
	}
	scope := auth.RunScope{
		TaskID: c.TaskID,
		ChatID: c.ID,
		Agent:  at.Name,
		Stage:  string(types.StageChat),
	}
	ttl := chatKeyTTL
	if delegatedUntil != nil {
		scope.DelegatedBy = c.DelegatedBy
		ttl = min(ttl, time.Until(*delegatedUntil))
	}
	raw, keyID, err := m.keys.MintRunKey(ctx, scope, c.WorkspaceID, ttl)
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.mint_run_key_failed", "chat", c.ID, "err", err))
		return
	}
	env[runKeyEnv] = raw
	m.keyMu.Lock()
	prev := m.keyByID[c.ID]
	m.keyByID[c.ID] = keyID
	m.keyMu.Unlock()
	m.revokeKeyID(ctx, prev)
}

// Every teardown path must go through here so a released container never leaves a live key.
func (m *Manager) markReleased(ctx context.Context, chatID string) error {
	m.keyMu.Lock()
	keyID := m.keyByID[chatID]
	delete(m.keyByID, chatID)
	m.keyMu.Unlock()
	m.revokeKeyID(ctx, keyID)
	return m.store.MarkReleased(chatID)
}

func (m *Manager) revokeKeyID(ctx context.Context, keyID string) {
	if m.keys == nil || keyID == "" {
		return
	}
	logx.Warn(m.keys.RevokeAPIKey(ctx, keyID), "chatruntime.log.revoke_run_key", "key", keyID)
}

func keyDelegation(c *chat.Chat, now time.Time) *time.Time {
	if !c.Delegated(now) {
		return nil
	}
	return c.DelegatedUntil
}

// Compared to the second: the column round-trips through Postgres.
func sameDelegation(held, want *time.Time) bool {
	if held == nil || want == nil {
		return held == nil && want == nil
	}
	return held.Truncate(time.Second).Equal(want.Truncate(time.Second))
}

// A failed read counts as no delegation: the container gets the ordinary key, the safe side.
func (m *Manager) withFreshDelegation(ctx context.Context, c *chat.Chat) *chat.Chat {
	if m.chats == nil {
		return c
	}
	out := *c
	fresh, err := m.chats.GetChat(ctx, c.ID)
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.reread_delegation_failed", "chat", c.ID, "err", err))
		out.DelegatedBy, out.DelegatedUntil = "", nil
		return &out
	}
	out.DelegatedBy, out.DelegatedUntil = fresh.DelegatedBy, fresh.DelegatedUntil
	return &out
}

// Call after the delegation is cleared in the store. Holding the lifecycle lock ensures an acquire
// that read
// the old delegation has finished minting before keys are revoked.
func (m *Manager) EndDelegation(ctx context.Context, chatID string) error {
	if m.keys == nil {
		return nil
	}
	mu := m.chatLock(chatID)
	mu.Lock()
	defer mu.Unlock()
	if err := m.keys.RevokeChatRunKeys(ctx, chatID); err != nil {
		return err
	}
	return m.evictLocked(ctx, chatID)
}

func (m *Manager) chatLock(chatID string) *sync.Mutex { return m.chatMu.Get(chatID) }

// Acquire returns the chat's runtime where the chat's auth places it now, so a profile moved to or
// from a machine, or a machine that went away, is seen before the runtime is reused.
func (m *Manager) Acquire(ctx context.Context, c *chat.Chat, at *types.AgentType, opts AcquireOpts) (string, error) {
	env, err := m.chatAuth(c, at)
	if err != nil {
		return "", err
	}
	return m.acquire(ctx, c, at, opts, env[runner.RunnerIDEnv])
}

// acquire takes the placement already decided: machine is a runner id, or "" for the container.
func (m *Manager) acquire(ctx context.Context, c *chat.Chat, at *types.AgentType, opts AcquireOpts, machine string) (string, error) {
	mu := m.chatLock(c.ID)
	mu.Lock()
	defer mu.Unlock()
	return m.acquireLocked(ctx, c, at, opts, machine)
}

func (m *Manager) acquireLocked(ctx context.Context, c *chat.Chat, at *types.AgentType, opts AcquireOpts, machine string) (string, error) {
	c = m.withFreshDelegation(ctx, c)
	delegatedUntil := keyDelegation(c, time.Now())
	if rt, err := m.store.Get(c.ID); err == nil && rt.Status != StatusEvicted && rt.Status != StatusFailed {
		sameKey := sameDelegation(rt.KeyDelegatedUntil, delegatedUntil)
		samePlace := machineOf(rt.ContainerID) == machine
		running := sameKey && samePlace && m.backendFor(rt.ContainerID).Running(ctx, rt.ContainerID)
		if running {
			logx.Warn(m.store.Touch(c.ID), "chatruntime.log.touch", "chat", c.ID)
			return rt.ContainerID, nil
		}
		switch {
		case !sameKey:
			log.Printf("[chat] %s", i18n.Log("chatruntime.log.delegation_changed", "chat", c.ID, "container", rt.ContainerID))
		case !samePlace:
			log.Printf("[chat] %s", i18n.Log("chatruntime.log.placement_changed", "chat", c.ID, "container", rt.ContainerID))
		default:
			log.Printf("[chat] %s", i18n.Log("chatruntime.log.container_not_running", "chat", c.ID, "container", rt.ContainerID))
		}
		m.backendFor(rt.ContainerID).Remove(ctx, rt.ContainerID)
		logx.Warn(m.markReleased(ctx, c.ID), "chatruntime.log.mark_released", "chat", c.ID)
	}

	unreserve, err := m.reserveCapacity(ctx, c.WorkspaceID)
	if err != nil {
		return "", err
	}
	// Keep the reservation until the row below is live; after that CountLive sees the container.
	defer unreserve()

	containerID, err := m.createContainer(ctx, c, at, opts, delegatedUntil, machine)
	if err != nil {
		logx.Warn(m.store.Upsert(&Runtime{
			ChatID: c.ID, WorkspaceID: c.WorkspaceID, AgentType: at.Type,
			Status: StatusFailed, CreatedAt: time.Now().UTC(), LastActivityAt: time.Now().UTC(),
		}), "chatruntime.log.mark_failed", "chat", c.ID)
		return "", err
	}
	now := time.Now().UTC()
	rt := &Runtime{
		ChatID: c.ID, WorkspaceID: c.WorkspaceID, AgentType: at.Type,
		ContainerID: containerID, Status: StatusReady, CreatedAt: now, LastActivityAt: now,
		KeyDelegatedUntil: delegatedUntil,
	}
	if err := m.store.Upsert(rt); err != nil {
		m.backendFor(containerID).Remove(ctx, containerID)
		return "", apperr.Wrap(err, "chatruntime.errors.store_upsert")
	}
	log.Printf("[chat] %s", i18n.Log("chatruntime.log.container_ready", "chat", c.ID, "container", containerID, "agent", at.Name))
	return containerID, nil
}

func (m *Manager) reserveCapacity(ctx context.Context, workspaceID string) (func(), error) {
	// Asked before capMu: it reads the database, and every other chat's reservation waits on capMu.
	if m.admit != nil {
		if err := m.admit(ctx, workspaceID); err != nil {
			return nil, err
		}
	}
	m.capMu.Lock()
	defer m.capMu.Unlock()
	if err := m.ensureCapacity(ctx, workspaceID); err != nil {
		return nil, err
	}
	if m.pending == nil {
		m.pending = map[string]int{}
	}
	m.pending[workspaceID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			m.capMu.Lock()
			defer m.capMu.Unlock()
			if m.pending[workspaceID] <= 1 {
				delete(m.pending, workspaceID)
				return
			}
			m.pending[workspaceID]--
		})
	}, nil
}

// Caller holds capMu.
func (m *Manager) pendingTotal() int {
	n := 0
	for _, v := range m.pending {
		n += v
	}
	return n
}

// Caller holds capMu. Only evicts the caller's own workspace (one tenant must never close
// another's),
// except chats idle past chat.idle_evict_seconds, which ReapIdle takes.
func (m *Manager) ensureCapacity(ctx context.Context, workspaceID string) error {
	maxLive, err := m.settings.ChatMax(ctx)
	if err != nil {
		maxLive = 5
	}
	live, err := m.store.CountLive()
	if err != nil {
		return apperr.Wrap(err, "chatruntime.errors.count_live")
	}
	live += m.pendingTotal()
	if live >= maxLive {
		m.ReapIdle(ctx)
		if live, err = m.store.CountLive(); err != nil {
			return apperr.Wrap(err, "chatruntime.errors.count_live")
		}
		live += m.pendingTotal()
		if live >= maxLive && !m.evictOwnOldest(ctx, workspaceID) {
			return ErrNoCapacity
		}
	}
	if m.workspaceLimit != nil {
		if limit := m.workspaceLimit(workspaceID); limit > 0 {
			n, err := m.store.CountLiveForWorkspace(workspaceID)
			if err != nil {
				return apperr.Wrap(err, "chatruntime.errors.count_workspace_live")
			}
			n += m.pending[workspaceID]
			if n >= limit && !m.evictOwnOldest(ctx, workspaceID) {
				return apperr.Unavailable(workspaceAtCapKey, "count", limit)
			}
		}
	}
	return nil
}

// An empty workspaceID evicts nothing: a chat with no tenant cannot prove which slots are its own.
func (m *Manager) evictOwnOldest(ctx context.Context, workspaceID string) bool {
	if workspaceID == "" {
		return false
	}
	live, err := m.store.LiveForWorkspace(workspaceID)
	if err != nil {
		return false
	}
	for _, rt := range live {
		if m.TurnInFlight(rt.ChatID) {
			continue
		}
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.evicting",
			"chat", rt.ChatID, "container", rt.ContainerID, "scope", "workspace "+workspaceID))
		m.backendFor(rt.ContainerID).Remove(ctx, rt.ContainerID)
		logx.Warn(m.markReleased(ctx, rt.ChatID), "chatruntime.log.mark_released", "chat", rt.ChatID)
		return true
	}
	return false
}

func (m *Manager) idleCutoff(ctx context.Context) time.Time {
	idleSecs, err := m.settings.ChatIdleEvictSeconds(ctx)
	if err != nil {
		idleSecs = 30 * 60
	}
	return time.Now().Add(-time.Duration(idleSecs) * time.Second)
}

// createContainer creates the runtime on machine, the placement the turn was resolved for. The
// create's own auth must agree: a turn resolved for a container carries a key no machine may get.
func (m *Manager) createContainer(
	ctx context.Context, c *chat.Chat, at *types.AgentType, opts AcquireOpts, delegatedUntil *time.Time, machine string,
) (string, error) {
	if m.augment != nil {
		at = m.augment(ctx, c.ID, at)
	}
	stage := string(types.StageChat)
	env := map[string]string{
		"AGENT_NAME":     at.Name,
		"INSTANCE_ID":    c.ID,
		"STAGE":          stage,
		"CHAT_ID":        c.ID,
		"OCTOPUS_API":    m.apiBaseURL,
		"MAX_TURNS":      at.TurnsForStage(stage),
		"OCTOPUS_LOCALE": i18n.FromContext(ctx),
	}
	if m.apiBaseURL == "" {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.no_api_url", "chat", c.ID))
	}
	if skills := at.DefaultSkills(stage); skills != "" {
		env["SKILLS"] = skills
	}
	if tools := at.DefaultMCP(stage); tools != "" {
		env["MCP_TOOLS"] = tools
	}
	// Must be set before the MCP env: a linked chat's servers get its task as ?task=. The auth
	// also reads it: a task's projects place its chat.
	if c.TaskID != "" {
		env["TASK_ID"] = c.TaskID
	}
	if at.Model != "" {
		env["MODEL"] = at.Model
	}
	if at.Effort != "" {
		env["EFFORT"] = at.Effort
	}
	if at.Engine != "" {
		env["ENGINE"] = at.Engine
	}
	// Must run before llmAuth: with the LLM gateway on, this key is what the CLI presents for the
	// model.
	m.applyRunKey(ctx, env, c, at, delegatedUntil)
	if m.llmAuth != nil {
		err := m.llmAuth(env, c.WorkspaceID, at.Name, at)
		if err == nil && env[runner.RunnerIDEnv] != machine {
			err = errPlacementMoved
		}
		if err != nil {
			m.keyMu.Lock()
			keyID := m.keyByID[c.ID]
			delete(m.keyByID, c.ID)
			m.keyMu.Unlock()
			m.revokeKeyID(ctx, keyID)
			return "", err
		}
	}
	b := m.backend
	if machine != "" {
		b = m.machine
	}
	// After the auth, which places the chat: the MCP grant records a chat on a machine.
	m.applyMCPEnv(ctx, c, at, env)

	return b.Create(ctx, CreateSpec{
		Chat:              c,
		AgentType:         at,
		Stage:             stage,
		Env:               env,
		Code:              opts.Code,
		WorkspaceReadOnly: true,
		Resources:         m.settings.ChatResourceCaps(ctx),
	})
}

// Never overrides keys already in env. The per-task thulu-sandbox URL is deliberately not wired: a
// chat must not drive a task's sandbox.
func (m *Manager) applyMCPEnv(ctx context.Context, c *chat.Chat, at *types.AgentType, env map[string]string) {
	switch {
	case env[runner.RunnerIDEnv] != "":
		if mcp.PrepareMachineEnv(env, m.noPerRunBrowser || m.browser.PerRun()) {
			log.Printf("[chat] %s", i18n.Log("chatruntime.log.machine_no_browser", "chat", c.ID, "agent", at.Name))
		}
	case m.noPerRunBrowser && browserrun.Granted(env["MCP_TOOLS"]):
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.no_per_run_browser", "chat", c.ID, "agent", at.Name))
	}
	mcp.ApplyLaunchEnv(ctx, env, m.mcpFwd, m.browser)
}

// Callers must hold the single-flight slot (BeginTurn); ExecTurn does not take it.
func (m *Manager) ExecTurn(ctx context.Context, c *chat.Chat, at *types.AgentType, opts AcquireOpts, text string) (*TurnResult, error) {
	timeoutSecs, err := m.settings.ChatTurnTimeoutSeconds(ctx)
	if err != nil || timeoutSecs <= 0 {
		timeoutSecs = 5 * 60
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()
	ctx, stop := context.WithCancelCause(ctx)
	m.cancels.Store(c.ID, stop)
	defer func() {
		m.cancels.Delete(c.ID)
		stop(nil)
	}()

	// Once per turn: the same answer places the chat and pays its engine.
	turn, err := m.resolveTurn(ctx, c, at)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		containerID, err := m.acquire(ctx, c, at, opts, turn.machine)
		if errors.Is(err, errPlacementMoved) && attempt == 0 {
			// The profile moved between the turn's auth and the create's: place the turn again.
			if turn, err = m.resolveTurn(ctx, c, at); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		res, err := m.execOnce(ctx, containerID, c, at, turn, text, opts.TurnID)
		if errors.Is(context.Cause(ctx), ErrTurnCanceled) {
			return nil, ErrTurnCanceled
		}
		if err == nil {
			logx.Warn(m.store.Touch(c.ID), "chatruntime.log.touch", "chat", c.ID)
			return res, nil
		}
		lastErr = err
		if !isContainerGoneErr(err) {
			return nil, err
		}
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.exec_dead_container", "chat", c.ID, "attempt", attempt+1))
		if lt := m.liveTurn(c.ID, opts.TurnID); lt != nil {
			lt.reset()
		}
		mu := m.chatLock(c.ID)
		mu.Lock()
		m.backendFor(containerID).Remove(ctx, containerID)
		logx.Warn(m.markReleased(ctx, c.ID), "chatruntime.log.mark_released", "chat", c.ID)
		mu.Unlock()
	}
	return nil, lastErr
}

// turnEnv is what the auth resolves a turn from. The ids name the run in a federation profile's
// identity token, and TASK_ID places a task's chat by its projects.
func turnEnv(c *chat.Chat, at *types.AgentType) map[string]string {
	env := map[string]string{"CHAT_ID": c.ID, "INSTANCE_ID": c.ID}
	if c.TaskID != "" {
		env["TASK_ID"] = c.TaskID
	}
	if at.Model != "" {
		env["MODEL"] = at.Model
	}
	if at.Effort != "" {
		env["EFFORT"] = at.Effort
	}
	if at.Engine != "" {
		env["ENGINE"] = at.Engine
	}
	return env
}

// turnAuth is what one turn runs with: its engine, the credential vars of its exec (only those
// leave the auth's env), its model and the machine it is placed on ("" for the container or pod).
type turnAuth struct {
	eng     engine.Engine
	env     []string
	model   string
	machine string
}

// Re-resolved per turn so a rotated key or re-bound profile (and its model) reaches an open chat.
func (m *Manager) resolveTurn(ctx context.Context, c *chat.Chat, at *types.AgentType) (*turnAuth, error) {
	env, err := m.chatAuth(c, at)
	if err != nil {
		return nil, err
	}
	engName := env["ENGINE"]
	if engName == "" {
		engName = at.Engine
	}
	eng, err := engine.Get(engName)
	if err != nil {
		return nil, apperr.Wrap(err, "chatruntime.errors.engine", "engine", engName)
	}
	turn := &turnAuth{eng: eng, model: env["MODEL"], machine: env[runner.RunnerIDEnv]}
	switch {
	case engine.HostAuthApplies(env):
		turn.env = eng.AuthEnv(ctx)
	case !engine.ViaGateway(env):
		turn.env = turnCredentialEnv(eng.Capabilities(), env)
	}
	return turn, nil
}

func turnCredentialEnv(caps engine.Capabilities, env map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(k string) {
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		// Absent vars are blanked so a credential of another kind left in the container env by
		// create cannot win.
		out = append(out, k+"="+env[k])
	}
	for _, kind := range caps.KeyKinds {
		for _, v := range kind.EnvVars {
			add(v)
		}
	}
	add(caps.AuthorizationEnv)
	if _, ok := env[caps.BaseURLEnv]; ok {
		add(caps.BaseURLEnv)
	}
	if _, ok := env[caps.OrgIDEnv]; ok {
		add(caps.OrgIDEnv)
	}
	if _, ok := env[caps.ProviderEnv]; ok {
		add(caps.ProviderEnv)
	}
	if _, ok := env["LLM_API_KEY"]; ok {
		add("LLM_API_KEY")
	}
	return out
}

func (m *Manager) execOnce(
	ctx context.Context, containerID string, c *chat.Chat, at *types.AgentType, turn *turnAuth, text, turnID string,
) (*TurnResult, error) {
	execEnv := make([]string, 0, len(turn.env)+4)
	execEnv = append(execEnv, turn.env...)
	execEnv = append(execEnv,
		"SESSION_ID="+c.SessionID,
		"CHAT_ID="+c.ID,
	)
	if turn.model != "" {
		execEnv = append(execEnv, "CHAT_MODEL="+turn.model)
	}
	execEnv = append(execEnv, "CHAT_MAX_TURNS="+at.TurnsForStage(string(types.StageChat)))

	// Every line goes to the live feed (and its transcript) and to the parser, as the engine
	// prints it. A backend that only returns stdout at the end feeds both then.
	feed := newTurnFeed(m.liveTurn(c.ID, turnID), turn.eng)
	lines := &lineWriter{sink: feed.line}

	start := time.Now()
	out, err := m.backendFor(containerID).Exec(ctx, ExecSpec{ContainerID: containerID, Env: execEnv, Stdin: text, Stdout: lines})
	if lines.n == 0 && len(out.Stdout) > 0 {
		lines.feed(out.Stdout)
	}
	lines.Flush()
	res := feed.close()
	if err != nil {
		return nil, err
	}
	res.DurationMs = time.Since(start).Milliseconds()

	if res.IsError {
		return nil, apperr.New("chatruntime.errors.turn_failed", "detail", strings.TrimSpace(res.Text))
	}
	if out.ExitCode != 0 && res.Text == "" {
		stderrTail := strings.TrimSpace(string(out.Stderr))
		if len(stderrTail) > 400 {
			stderrTail = stderrTail[len(stderrTail)-400:]
		}
		return nil, apperr.New("chatruntime.errors.turn_script_exited", "code", out.ExitCode, "stderr", stderrTail)
	}
	return res, nil
}

func (m *Manager) Evict(ctx context.Context, chatID string) error {
	mu := m.chatLock(chatID)
	mu.Lock()
	defer mu.Unlock()
	return m.evictLocked(ctx, chatID)
}

func (m *Manager) evictLocked(ctx context.Context, chatID string) error {
	rt, err := m.store.Get(chatID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if rt.ContainerID != "" {
		m.backendFor(rt.ContainerID).Remove(ctx, rt.ContainerID)
	}
	if rt.Status != StatusEvicted {
		return m.markReleased(ctx, chatID)
	}
	return nil
}

func (m *Manager) DropState(ctx context.Context, chatID string) error {
	if err := m.Evict(ctx, chatID); err != nil {
		return err
	}
	if err := m.store.Delete(chatID); err != nil {
		return err
	}
	if m.dataDir == "" {
		return nil
	}
	dir := filepath.Join(m.dataDir, "chats", chatID)
	return os.RemoveAll(dir)
}

func (m *Manager) ReapIdle(ctx context.Context) {
	cutoff := m.idleCutoff(ctx)
	for {
		rt, err := m.store.IdleEvictionCandidate(cutoff, "")
		if err != nil || rt == nil {
			return
		}
		if m.TurnInFlight(rt.ChatID) {
			// Touch it so the candidate query moves past it; otherwise this loop never ends.
			logx.Warn(m.store.Touch(rt.ChatID), "chatruntime.log.touch_in_flight", "chat", rt.ChatID)
			continue
		}
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.idle_evicting", "chat", rt.ChatID, "container", rt.ContainerID))
		m.backendFor(rt.ContainerID).Remove(ctx, rt.ContainerID)
		logx.Warn(m.markReleased(ctx, rt.ChatID), "chatruntime.log.mark_released", "chat", rt.ChatID)
	}
}

// staleTurnSweeper kills what turns of a daemon that is gone left in a chat container: the turns
// themselves and what they started in the background. Only that daemon's memory knew of them.
// SweepStaleTurns reports whether the container stays; one that cannot be swept is removed.
type staleTurnSweeper interface {
	SweepStaleTurns(ctx context.Context, containerID string) bool
}

// Reconcile runs at boot: it releases the chats whose container is gone, removes the containers no
// chat claims, and sweeps the turns the daemon before left running in the containers it keeps.
func (m *Manager) Reconcile(ctx context.Context) {
	if !m.backend.Available() {
		return
	}
	live, err := m.store.ListLive()
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.reconcile_list_failed", "err", err))
		return
	}
	liveByContainer := map[string]string{}
	for _, rt := range live {
		// A machine chat has nothing to find here; after a restart its next turn recreates it.
		if machineOf(rt.ContainerID) != "" {
			continue
		}
		if !m.backend.Running(ctx, rt.ContainerID) {
			log.Printf("[chat] %s", i18n.Log("chatruntime.log.reconcile_container_gone", "chat", rt.ChatID, "container", rt.ContainerID))
			m.backend.Remove(ctx, rt.ContainerID)
			logx.Warn(m.markReleased(ctx, rt.ChatID), "chatruntime.log.mark_released", "chat", rt.ChatID)
			continue
		}
		if sw, ok := m.backend.(staleTurnSweeper); ok && !m.TurnInFlight(rt.ChatID) && !sw.SweepStaleTurns(ctx, rt.ContainerID) {
			logx.Warn(m.markReleased(ctx, rt.ChatID), "chatruntime.log.mark_released", "chat", rt.ChatID)
			continue
		}
		liveByContainer[rt.ContainerID] = rt.ChatID
	}

	managed, err := m.backend.ListManaged(ctx)
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.reconcile_list_managed_failed", "err", err))
		return
	}
	for _, id := range managed {
		if _, ok := liveByContainer[id]; ok {
			continue
		}
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.reconcile_remove_orphan", "container", id))
		m.backend.Remove(ctx, id)
	}
}

func (m *Manager) StartGC(ctx context.Context, interval time.Duration) {
	go syncx.TickUntil(ctx, interval, m.ReapIdle)
	every := m.orphanEvery
	if every <= 0 {
		every = orphanSweepInterval
	}
	go syncx.TickUntil(ctx, every, func(ctx context.Context) { m.ReapOrphans(ctx) })
}

// ReapOrphans removes the chat containers (pods in Kubernetes) no live row claims: one whose row
// was released while its remove failed, or one a create left after its teardown failed. It lists
// the containers before the rows, and stands down while a create is in flight, whose container
// exists before its row says so.
func (m *Manager) ReapOrphans(ctx context.Context) int {
	if !m.backend.Available() || m.creating() {
		return 0
	}
	managed, err := m.backend.ListManaged(ctx)
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.reconcile_list_managed_failed", "err", err))
		return 0
	}
	if len(managed) == 0 {
		return 0
	}
	live, err := m.store.ListLive()
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.reconcile_list_failed", "err", err))
		return 0
	}
	claimed := make(map[string]bool, len(live))
	for _, rt := range live {
		claimed[rt.ContainerID] = true
	}
	removed := 0
	for _, id := range managed {
		if claimed[id] {
			continue
		}
		if m.creating() {
			return removed
		}
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.reconcile_remove_orphan", "container", id))
		m.backend.Remove(ctx, id)
		removed++
	}
	return removed
}

// creating reports a create in flight: its reservation lasts until the row records the container.
func (m *Manager) creating() bool {
	m.capMu.Lock()
	defer m.capMu.Unlock()
	return m.pendingTotal() > 0
}

func isContainerGoneErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errChatPodNeedsRecreate) {
		return true
	}
	s := err.Error()
	//i18n:ignore matches the Docker engine's error text ("container ... is not running"), not a message
	return strings.Contains(s, "is not running") ||
		//i18n:ignore matches the Docker engine's error text ("No such container"), not a message
		strings.Contains(strings.ToLower(s), "no such container")
}
