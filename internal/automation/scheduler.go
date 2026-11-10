package automation

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// next_run_at is advanced in the same UPDATE that claims the firing, so a crash neither loses nor
// replays it and a second replica loses the race instead of double-firing.
type Scheduler struct {
	store  *Store
	engine *Engine

	interval  time.Duration
	batch     int
	nudge     chan struct{}
	eventTick time.Duration

	wg sync.WaitGroup
}

func NewScheduler(store *Store, engine *Engine) *Scheduler {
	return &Scheduler{
		store:     store,
		engine:    engine,
		interval:  10 * time.Second,
		eventTick: 2 * time.Second,
		batch:     25,
		nudge:     make(chan struct{}, 1),
	}
}

// Not zero: the loop ticks every 10s so every firing is a few seconds late. Not hours: after
// downtime stale sweeps should not replay.
const defaultStartingDeadline = 2 * time.Minute

func (s *Scheduler) Start(ctx context.Context) {
	if n, err := s.store.ReconcileRunning(ctx); err != nil {
		log.Printf("[automation] %s", i18n.Log("automation.log.reconcile_failed", "err", err))
	} else if n > 0 {
		log.Printf("[automation] %s", i18n.Log("automation.log.parked_runs", "count", n))
	}

	go s.scheduleLoop(ctx)
	go s.eventLoop(ctx)
}

func (s *Scheduler) Wait() { s.wg.Wait() }

func (s *Scheduler) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

func (s *Scheduler) scheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil {
			log.Printf("[automation] %s", i18n.Log("automation.log.tick_failed", "err", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.nudge:
		}
	}
}

func (s *Scheduler) eventLoop(ctx context.Context) {
	ticker := time.NewTicker(s.eventTick)
	defer ticker.Stop()
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.ProcessEvents(ctx); err != nil {
				log.Printf("[automation] %s", i18n.Log("automation.log.process_events_failed", "err", err))
			}
			if _, err := s.FlushDebounced(ctx); err != nil {
				log.Printf("[automation] %s", i18n.Log("automation.log.flush_debounced_failed", "err", err))
			}
		case <-purge.C:
			if n, err := s.store.PurgeEvents(ctx, time.Now().Add(-7*24*time.Hour)); err != nil {
				log.Printf("[automation] %s", i18n.Log("automation.log.purge_events_failed", "err", err))
			} else if n > 0 {
				log.Printf("[automation] %s", i18n.Log("automation.log.purged_events", "count", n))
			}
		}
	}
}

func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	due, err := s.store.ClaimDue(ctx, now, s.batch)
	if err != nil {
		return 0, err
	}
	fired := 0
	for i := range due {
		a := due[i]
		if a.Parsed == nil {
			log.Printf("[automation] %s", i18n.Log("automation.log.unparseable_spec", "name", a.Name, "id", a.ID))
			continue
		}
		switch a.TriggerKind {
		case TriggerMetric:
			ok, payload, mErr := s.evaluateMetric(ctx, &a)
			if mErr != nil {
				log.Printf("[automation] %s", i18n.Log("automation.log.metric_trigger_failed", "id", a.ID, "err", mErr))
				continue
			}
			if !ok {
				continue
			}
			if s.fire(ctx, &a, Firing{Trigger: TriggerMetric, Payload: payload}, a.DueAt) {
				fired++
			}
		default:
			if s.fire(ctx, &a, Firing{Trigger: TriggerSchedule}, a.DueAt) {
				fired++
			}
		}
	}
	return fired, nil
}

func (s *Scheduler) fire(ctx context.Context, a *Automation, f Firing, due time.Time) bool {
	if reason, ok := s.blocked(ctx, a, due); !ok {
		if _, err := s.store.RecordSkip(ctx, a, f.Trigger, reason); err != nil {
			log.Printf("[automation] %s", i18n.Log("automation.log.record_skip_failed", "id", a.ID, "err", err))
		}
		return false
	}
	s.start(ctx, a, f)
	return true
}

func (s *Scheduler) start(ctx context.Context, a *Automation, f Firing) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if _, err := s.engine.Execute(context.WithoutCancel(ctx), a, f); err != nil {
			log.Printf("[automation] %s", i18n.Log("automation.log.run_failed", "id", a.ID, "err", err))
		}
	}()
}

func (s *Scheduler) blocked(ctx context.Context, a *Automation, due time.Time) (reason Msg, ok bool) {
	deadline := a.Parsed.Policy.StartingDeadline.Duration()
	if deadline <= 0 {
		deadline = defaultStartingDeadline
	}
	if late := time.Since(due); late > deadline {
		return msgText("automation.runs.missed_deadline", "late", late.Round(time.Second), "deadline", deadline), false
	}

	running, err := s.store.RunningCount(ctx, a.ID)
	if err != nil {
		return msgErr(apperr.New("automation.runs.running_check_failed", "err", err)), false
	}
	if running == 0 {
		return nil, true
	}
	switch a.Parsed.Policy.Concurrency {
	case ConcurrencyAllow:
		return nil, true
	case ConcurrencyReplace:
		ids, err := s.store.RunningRuns(ctx, a.ID)
		if err != nil {
			return msgErr(apperr.New("automation.runs.running_list_failed", "err", err)), false
		}
		for _, id := range ids {
			logx.Warn(s.engine.Cancel(ctx, id), "automation.log.cancel_for_replace", "run", id)
		}
		return nil, true
	default:
		return msgText("automation.runs.still_going", "count", running), false
	}
}

func (s *Scheduler) RunNow(ctx context.Context, a *Automation, payload map[string]any) (*Run, error) {
	if a.Parsed == nil {
		return nil, apperr.Unprocessable("automation.errors.spec_unparseable", "id", a.ID)
	}
	trigger := TriggerManual
	if payload != nil {
		trigger = TriggerTest
	}
	if reason, ok := s.blocked(ctx, a, time.Now()); !ok {
		return s.store.RecordSkip(ctx, a, trigger, reason)
	}
	return s.engine.Execute(ctx, a, Firing{Trigger: trigger, Payload: payload})
}

func (s *Scheduler) ProcessEvents(ctx context.Context) (int, error) {
	events, err := s.store.ClaimEvents(ctx, s.batch)
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, e := range events {
		fired += s.deliver(ctx, e)
	}
	return fired, nil
}

func (s *Scheduler) deliver(ctx context.Context, e Event) int {
	subs, err := s.store.ListByEvent(ctx, e.Kind, e.WorkspaceID)
	if err != nil {
		log.Printf("[automation] %s", i18n.Log("automation.log.subscribers_failed", "kind", e.Kind, "err", err))
		return 0
	}
	if len(subs) > 0 {
		e = s.withOrigin(ctx, e)
	}
	fired := 0
	for i := range subs {
		if s.deliverTo(ctx, &subs[i], e) {
			fired++
		}
	}
	return fired
}

func (s *Scheduler) deliverTo(ctx context.Context, a *Automation, e Event) bool {
	if a.Parsed == nil || ownEvent(a, e) || !s.filterMatches(ctx, a, e) {
		return false
	}
	if skip := chainGate(e); skip != nil {
		if _, recErr := s.store.RecordSkip(ctx, a, TriggerEvent, skip); recErr != nil {
			log.Printf("[automation] %s", i18n.Log("automation.log.record_skip_failed", "id", a.ID, "err", recErr))
		}
		return false
	}
	if window := a.Parsed.Policy.Debounce.Duration(); window > 0 && e.DedupeKey != "" {
		if bufErr := s.store.BufferEvent(ctx, a, e, window); bufErr != nil {
			log.Printf("[automation] %s", i18n.Log("automation.log.buffer_event_failed", "id", a.ID, "err", bufErr))
		}
		return false
	}
	f := Firing{Trigger: TriggerEvent, Payload: e.Payload, DedupeKey: e.DedupeKey}
	return s.fire(ctx, a, f, time.Now())
}

// Only task.created carries provenance from the publisher; without this stamp a recipe whose task
// moves into the stage it watches fires on its own output forever.
func (s *Scheduler) withOrigin(ctx context.Context, e Event) Event {
	taskID, ok := e.Payload["task_id"].(string)
	if !ok || taskID == "" {
		return e
	}
	if id, stamped := e.Payload[PayloadAutomationID].(string); stamped && id != "" {
		return e
	}
	automationID, depth, ok, err := s.store.TaskOrigin(ctx, taskID)
	if err != nil {
		log.Printf("[automation] %s", i18n.Log("automation.log.task_origin_failed", "task", taskID, "err", err))
		return e
	}
	if !ok {
		return e
	}
	payload := make(map[string]any, len(e.Payload)+2)
	for k, v := range e.Payload {
		payload[k] = v
	}
	payload[PayloadAutomationID] = automationID
	payload[PayloadChainDepth] = depth
	e.Payload = payload
	return e
}

func ownEvent(a *Automation, e Event) bool {
	id, ok := e.Payload[PayloadAutomationID].(string)
	return ok && id != "" && id == a.ID
}

// Runs after the filter so recipes the filter rejects get no skip noise.
func chainGate(e Event) Msg {
	if ChainDepthOf(e.Payload) >= MaxChainDepth {
		return msgText("automation.runs.chain_too_deep", "max", MaxChainDepth)
	}
	return nil
}

func (s *Scheduler) filterMatches(ctx context.Context, a *Automation, e Event) bool {
	filter := a.Parsed.Trigger.Filter
	if filter == "" {
		return true
	}
	ok, err := evalCondition(filter, map[string]any{
		"trigger": map[string]any{"kind": e.Kind, "payload": e.Payload},
		"vars":    varsOf(a.Parsed),
	})
	if err != nil {
		reason := msgErr(apperr.New("automation.runs.filter_failed", "filter", filter, "err", err))
		if _, recErr := s.store.RecordSkip(ctx, a, TriggerEvent, reason); recErr != nil {
			log.Printf("[automation] %s", i18n.Log("automation.log.record_skip_failed", "id", a.ID, "err", recErr))
		}
		return false
	}
	return ok
}

func (s *Scheduler) FlushDebounced(ctx context.Context) (int, error) {
	due, err := s.store.ClaimDebounced(ctx, time.Now(), s.batch)
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, d := range due {
		a, getErr := s.store.Get(ctx, d.AutomationID, InWorkspace(d.WorkspaceID))
		if getErr != nil || a.Parsed == nil || !a.Enabled {
			continue
		}
		if _, ok := s.blocked(ctx, a, time.Now()); !ok {
			// Requeue instead of skipping: a skip would drop every event the window collected.
			if reqErr := s.store.RequeueDebounced(ctx, d, time.Now().Add(s.eventTick)); reqErr != nil {
				log.Printf("[automation] %s", i18n.Log("automation.log.requeue_debounce_failed", "id", a.ID, "err", reqErr))
			}
			continue
		}
		s.start(ctx, a, Firing{Trigger: TriggerEvent, Payload: d.TriggerPayload(), DedupeKey: d.DedupeKey})
		fired++
	}
	return fired, nil
}

func (s *Scheduler) evaluateMetric(ctx context.Context, a *Automation) (bool, map[string]any, error) {
	m := a.Parsed.Trigger.Metric
	if m == nil {
		return false, nil, apperr.Invalid("automation.errors.metric_trigger_missing")
	}
	if s.engine.deps.Metrics == nil {
		return false, nil, apperr.Unavailable("automation.errors.metric_trigger_unavailable")
	}
	agg := m.Agg
	if agg == "" {
		agg = "avg"
	}
	value, hasData, err := s.engine.deps.Metrics.Aggregate(ctx, a.WorkspaceID, m.Environment, m.Metric, m.Window.Duration(), agg)
	if err != nil {
		return false, nil, err
	}
	if !hasData {
		// No data is not below threshold: leave the firing flag alone so a collection gap does not
		// re-arm a burning incident.
		return false, nil, nil
	}
	over := false
	switch m.Op {
	case ">":
		over = value > m.Threshold
	case ">=":
		over = value >= m.Threshold
	case "<":
		over = value < m.Threshold
	case "<=":
		over = value <= m.Threshold
	}
	wasFiring, err := s.store.IsFiring(ctx, a.ID)
	if err != nil {
		return false, nil, err
	}
	if !over {
		if wasFiring {
			logx.Warn(s.store.SetFiring(ctx, a.ID, false), "automation.log.clear_firing", "id", a.ID)
		}
		return false, nil, nil
	}
	if wasFiring {
		return false, nil, nil
	}
	if err := s.store.SetFiring(ctx, a.ID, true); err != nil {
		return false, nil, err
	}
	return true, map[string]any{
		"environment": m.Environment,
		"metric":      m.Metric,
		"value":       value,
		"threshold":   m.Threshold,
		"op":          m.Op,
		"window":      m.Window.String(),
		"agg":         agg,
	}, nil
}

type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

func (s *Scheduler) Publish(ctx context.Context, e Event) error {
	if !KnownEvent(e.Kind) {
		return apperr.Invalid("automation.errors.unknown_event_kind", "kind", e.Kind)
	}
	if _, err := s.store.PublishEvent(ctx, e); err != nil {
		return err
	}
	return nil
}
