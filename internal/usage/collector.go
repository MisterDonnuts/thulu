package usage

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"

	// Registers the default engine so engine.MustGet("") resolves.
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/metrics"
)

type Collector struct {
	docker  *client.Client
	store   *Store
	dataDir string
}

func NewCollector(docker *client.Client, store *Store, dataDir string) *Collector {
	logsDir := filepath.Join(dataDir, "logs")
	logx.Warn(os.MkdirAll(logsDir, 0o750), "usage.log.create_logs_dir", "path", logsDir)
	return &Collector{docker: docker, store: store, dataDir: dataDir}
}

func (c *Collector) Store() *Store { return c.store }

func (c *Collector) Collect(ctx context.Context, a *agent.Agent) {
	fullLogs, ok := c.readAllLogs(ctx, a)
	if !ok {
		return
	}
	c.RecordRun(ctx, a, fullLogs)
}

// Must not touch c.docker: the K8s daemon builds the Collector with a nil Docker client.
func (c *Collector) RecordRun(ctx context.Context, a *agent.Agent, fullLogs string) {
	// Use the full run id, never a prefix: K8s pod names share their first 8 chars per agent, so a
	// prefix collides log names.
	// containerIDFromLogName relies on run ids never containing '_'.
	logFileName := fmt.Sprintf("%s_%s_%s.log",
		a.Name,
		a.StartedAt.Format("2006-01-02_15-04-05"),
		a.ID,
	)
	logPath := filepath.Join(c.dataDir, "logs", logFileName)
	if err := os.WriteFile(logPath, []byte(fullLogs), 0o644); err != nil {
		log.Printf("[usage] %s", i18n.Log("usage.log.save_logs_failed", "agent", a.Name, "err", err))
	}

	c.recordSessionFromLogs(ctx, a, fullLogs, logPath, logFileName)
}

func (c *Collector) readAllLogs(ctx context.Context, a *agent.Agent) (string, bool) {
	reader, err := c.docker.ContainerLogs(ctx, a.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
	})
	if err != nil {
		log.Printf("[usage] %s", i18n.Log("usage.log.read_logs_failed", "agent", a.Name, "err", err))
		return "", false
	}
	defer reader.Close()

	return dockerx.DemuxAll(reader), true
}

func (c *Collector) recordSessionFromLogs(ctx context.Context, a *agent.Agent, fullLogs, logPath, logFileName string) {
	eng := engineForAgent(a)
	var (
		model         string
		lastRateLimit *engine.RateLimit
		serviceTier   string
		turnIndex     int
		toolEvents    []ToolEvent
		events        []engine.Event
		lastMessage   string
		total         *Session
		now           = time.Now().UTC()
	)

	for ev := range eng.ParseStream(strings.NewReader(fullLogs)) {
		events = append(events, ev)
		switch ev.Kind {
		case engine.EventInit:
			model = ev.Model
		case engine.EventAssistant:
			// Claude Code prints one assistant line per content block; blocks of one API call share
			// the message id.
			if ev.MessageID == "" || ev.MessageID != lastMessage {
				turnIndex++
				lastMessage = ev.MessageID
			}
			if ev.Usage != nil && ev.Usage.ServiceTier != "" {
				serviceTier = ev.Usage.ServiceTier
			}
		case engine.EventToolUse:
			toolEvents = append(toolEvents, toolEventFrom(a.ID, turnIndex, now, ev))
		case engine.EventRateLimit:
			lastRateLimit = ev.RateLimit
		case engine.EventResult:
			// One result per user message: a steered run answers each one. Merge them; stopping at
			// the first bills only the opening turn.
			turn := sessionFromResult(a, ev, model, logPath)
			turn.ServiceTier = serviceTier
			total = mergeSession(total, turn)
		case engine.EventUnknown, engine.EventToolResult, engine.EventUserMessage:
		}
	}
	if total == nil {
		log.Printf("[usage] %s", i18n.Log("usage.log.no_result_event", "agent", a.Name))
		return
	}
	sess := *total
	c.persistRun(ctx, a, sess, events, toolEvents, fullLogs, logFileName)
	wid := sess.WorkspaceID
	if wid == "" {
		wid = c.store.RunWorkspace(ctx, sess.TaskID, sess.ProjectID)
	}
	c.recordRateLimit(wid, lastRateLimit)
}

func (c *Collector) persistRun(ctx context.Context, a *agent.Agent, sess Session,
	events []engine.Event, toolEvents []ToolEvent, fullLogs, logFileName string,
) {
	added := c.store.Add(ctx, sess)
	if added != nil {
		log.Printf("[usage] %s", i18n.Log("usage.log.store_session_failed", "agent", a.Name, "err", added))
	} else {
		log.Printf("[usage] %s", i18n.Log("usage.log.session_recorded", "agent", a.Name, "turns", sess.Turns,
			"cost", sess.CostUSD, "tools", len(toolEvents), "file", logFileName))
	}
	metrics.RecordAgentRun(a.Type, sess.StopReason)
	metrics.RecordTokenUsage(sess.Model,
		int64(sess.InputTokens), int64(sess.OutputTokens),
		int64(sess.CacheRead), int64(sess.CacheCreation), sess.CostUSD)
	// SaveAttribution writes the tool events itself; plain rows are only the fallback, so never
	// write both.
	attributed := false
	if added == nil {
		if err := c.store.SaveAttribution(ctx, sess.ID, events, TokenOptimizerLevel(fullLogs)); err != nil {
			logx.Warn(err, "usage.log.attribution_failed", "agent", a.Name)
		} else {
			attributed = true
		}
	}
	if !attributed && len(toolEvents) > 0 {
		if err := c.store.AddToolEvents(ctx, toolEvents); err != nil {
			log.Printf("[usage] %s", i18n.Log("usage.log.store_tool_events_failed", "agent", a.Name, "err", err))
		}
	}
}

func mergeSession(total *Session, turn Session) *Session {
	if total == nil {
		t := turn
		return &t
	}
	total.DurationMs += turn.DurationMs
	total.Turns += turn.Turns
	total.CostUSD += turn.CostUSD
	total.InputTokens += turn.InputTokens
	total.OutputTokens += turn.OutputTokens
	total.CacheRead += turn.CacheRead
	total.CacheCreation += turn.CacheCreation
	total.FinishedAt = turn.FinishedAt
	total.StopReason = turn.StopReason
	if turn.Model != "" {
		total.Model = turn.Model
	}
	if turn.ServiceTier != "" {
		total.ServiceTier = turn.ServiceTier
	}
	return total
}

func engineForAgent(a *agent.Agent) engine.Engine {
	return engine.MustGet(a.Engine)
}

func sessionFromResult(a *agent.Agent, ev engine.Event, model, logPath string) Session {
	sess := Session{
		ID:         a.ID,
		Agent:      a.Name,
		AgentType:  a.Type,
		Engine:     a.Engine,
		Model:      model,
		StartedAt:  a.StartedAt,
		FinishedAt: time.Now().UTC(),
		DurationMs: ev.DurationMs,
		Turns:      ev.NumTurns,
		CostUSD:    ev.CostUSD,
		StopReason: ev.StopReason,
		LogsFile:   logPath,
		OutputDir:  a.OutputDir,
		TaskID:     a.TaskID,
		ProjectID:  a.ProjectID,
		Stage:      a.Stage,
	}
	if ev.Usage != nil {
		sess.InputTokens = ev.Usage.InputTokens
		sess.OutputTokens = ev.Usage.OutputTokens
		sess.CacheRead = ev.Usage.CacheRead
		sess.CacheCreation = ev.Usage.CacheCreation
		if ev.Usage.ServiceTier != "" {
			sess.ServiceTier = ev.Usage.ServiceTier
		}
	}
	return sess
}

func (c *Collector) recordRateLimit(workspaceID string, rl *engine.RateLimit) {
	if rl == nil {
		return
	}
	c.store.SetRateLimit(workspaceID, rateLimitFromEngine(*rl))
	log.Printf("[usage] %s", i18n.Log("usage.log.rate_limit", "percent", rl.Utilization*100, "resets", rl.ResetsAt))
}

func rateLimitFromEngine(rl engine.RateLimit) RateLimit {
	return RateLimit{
		Status:        rl.Status,
		ResetsAt:      rl.ResetsAt,
		RateLimitType: rl.RateLimitType,
		Utilization:   rl.Utilization,
		IsOverage:     rl.IsOverage,
	}
}
