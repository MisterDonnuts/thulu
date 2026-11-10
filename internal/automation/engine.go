package automation

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type Engine struct {
	store *Store
	deps  Deps

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func NewEngine(store *Store, deps Deps) *Engine {
	return &Engine{store: store, deps: deps, running: map[string]context.CancelFunc{}}
}

var errStop error = apperr.New("automation.errors.stopped_by_condition")

var ErrBudgetExhausted error = apperr.RateLimited("automation.errors.budget_exhausted")

type Firing struct {
	Trigger   string
	DedupeKey string
	Payload   map[string]any
}

func (e *Engine) Execute(ctx context.Context, a *Automation, f Firing) (*Run, error) {
	if a.Parsed == nil {
		return nil, apperr.Unprocessable("automation.errors.spec_unparseable", "id", a.ID)
	}
	payloadJSON := "{}"
	if f.Payload != nil {
		if b, err := json.Marshal(f.Payload); err == nil {
			payloadJSON = string(b)
		}
	}
	run, err := e.store.StartRun(ctx, a, f, payloadJSON)
	if err != nil {
		return nil, err
	}

	timeout := a.Parsed.Policy.Timeout.Duration()
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	e.mu.Lock()
	e.running[run.ID] = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.running, run.ID)
		e.mu.Unlock()
	}()

	firedAt := time.Now().UTC()
	kind := f.Trigger
	if kind == TriggerTest {
		kind = a.Parsed.Trigger.Kind()
	}
	state := &runState{
		automation: a,
		run:        run,
		depth:      ChainDepthOf(f.Payload),
		ctx: map[string]any{
			"trigger": map[string]any{
				"kind":     kind,
				"fired_at": firedAt.Format(time.RFC3339),
				// Firing time, not now: a run starting at 23:59:59 must not reach the next day's
				// fingerprint card.
				"fired_date": firedAt.Format(time.DateOnly),
				"payload":    f.Payload,
			},
			"vars":  varsOf(a.Parsed),
			"steps": map[string]any{},
			"run": map[string]any{
				"id": run.ID, "automation_id": a.ID, "workspace_id": a.WorkspaceID,
			},
		},
	}

	if e.needsKey(a.Parsed) && e.deps.Keys != nil {
		raw, keyID, mintErr := e.deps.Keys.MintAutomationKey(ctx, a.WorkspaceID, a.ID, run.ID, timeout+time.Minute)
		if mintErr != nil {
			log.Printf("[automation] %s", i18n.Log("automation.log.mint_key_failed", "id", a.ID, "err", mintErr))
		} else {
			state.apiKey = raw
			defer func() {
				logx.Warn(e.deps.Keys.RevokeAPIKey(ctx, keyID), "automation.log.revoke_run_key", "key", keyID)
			}()
		}
	}

	runErr := e.runSteps(runCtx, state, a.Parsed.Steps)

	if state.usedSandbox && e.deps.Scripts != nil {
		logx.Warn(e.deps.Scripts.ReleaseSandbox(context.WithoutCancel(ctx), state.owner()),
			"automation.log.release_sandbox", "run", run.ID)
	}

	switch {
	case runErr == nil, errors.Is(runErr, errStop):
		run.Status = RunSuccess
		if errors.Is(runErr, errStop) && state.stopReason != nil {
			run.ReasonMsg = state.stopReason
		}
	// An exhausted budget is a working limit, so the run is skipped, not failed.
	case errors.Is(runErr, ErrBudgetExhausted):
		run.Status = RunSkipped
		run.ReasonMsg = msgErr(runErr)
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		run.Status = RunTimeout
		run.ErrorMsg = msgText("automation.runs.timeout", "timeout", timeout)
	case errors.Is(runCtx.Err(), context.Canceled):
		run.Status = RunCanceled
		run.ErrorMsg = msgErr(runErr)
	default:
		run.Status = RunFailed
		run.ErrorMsg = msgErr(runErr)
	}
	if err := e.store.FinishRun(context.WithoutCancel(ctx), run); err != nil {
		return run, err
	}
	logx.Warn(e.store.TrimHistory(context.WithoutCancel(ctx), a.ID, a.Parsed.Policy.HistoryLimit),
		"automation.log.trim_history", "id", a.ID)
	return run, nil
}

func (e *Engine) Cancel(ctx context.Context, runID string) error {
	e.mu.Lock()
	cancel, ok := e.running[runID]
	e.mu.Unlock()
	if ok {
		cancel()
	}
	return e.store.CancelRun(ctx, runID)
}

func (e *Engine) needsKey(spec *Spec) bool {
	var walk func(steps []Step) bool
	walk = func(steps []Step) bool {
		for i := range steps {
			if steps[i].MCP != nil {
				return true
			}
			if walk(steps[i].Steps) {
				return true
			}
		}
		return false
	}
	return walk(spec.Steps)
}

func varsOf(spec *Spec) map[string]any {
	if spec.Vars == nil {
		return map[string]any{}
	}
	return spec.Vars
}

type runState struct {
	automation  *Automation
	run         *Run
	ctx         map[string]any
	seq         int
	apiKey      string
	usedSandbox bool
	stopReason  Msg
	depth       int
}

func (s *runState) owner() string { return "auto:" + s.run.ID }

func (s *runState) stepResults() map[string]any {
	results, ok := s.ctx["steps"].(map[string]any)
	if !ok {
		results = map[string]any{}
		s.ctx["steps"] = results
	}
	return results
}

func (s *runState) setResult(id string, result map[string]any) {
	s.stepResults()[id] = result
}

func (e *Engine) runSteps(ctx context.Context, st *runState, steps []Step) error {
	for i := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.runStep(ctx, st, &steps[i]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runStep(ctx context.Context, st *runState, step *Step) (err error) {
	kind := step.Kind()
	st.seq++
	row := &RunStep{
		RunID: st.run.ID, Seq: st.seq, StepID: step.ID, Kind: kind,
		StartedAt: time.Now().UTC(),
	}
	defer func() {
		if r := recover(); r != nil {
			err = apperr.Internal("automation.errors.step_panicked", "step", step.ID, "panic", r)
			row.Status = StepFailed
			row.ErrorMsg = msgErr(err)
			e.finishStepRow(ctx, row)
		}
	}()

	if step.When != "" {
		ok, whenErr := evalCondition(step.When, st.ctx)
		if whenErr != nil {
			row.Status = StepFailed
			row.ErrorMsg = msgErr(whenErr)
			e.finishStepRow(ctx, row)
			return apperr.Wrap(whenErr, "automation.errors.step_when_failed", "step", step.ID)
		}
		if !ok {
			row.Status = StepSkipped
			row.OutputMsg = msgText("automation.runs.when_false")
			e.finishStepRow(ctx, row)
			st.setResult(step.ID, map[string]any{"status": StepSkipped})
			return nil
		}
	}

	result, stepErr := e.dispatch(ctx, st, step, row)
	if result == nil {
		result = map[string]any{}
	}

	switch {
	case stepErr == nil:
		row.Status = StepOK
		result["status"] = StepOK
	case errors.Is(stepErr, errStop):
		row.Status = StepOK
		result["status"] = StepOK
	case errors.Is(stepErr, ErrBudgetExhausted):
		row.Status = StepSkipped
		row.ErrorMsg = msgErr(stepErr)
		result["status"] = StepSkipped
		result["reason"] = row.ErrorMsg.Text()
	default:
		row.Status = StepFailed
		row.ErrorMsg = msgErr(stepErr)
		result["status"] = StepFailed
		result["error"] = row.ErrorMsg.Text()
	}
	st.setResult(step.ID, result)
	if row.Output == "" && row.OutputMsg == nil {
		row.Output = summarize(result)
		row.OutputMsg = summarizeIn(result, row.ErrorMsg)
	}
	e.finishStepRow(ctx, row)

	// The budget ends the run even with continue_on_error.
	if errors.Is(stepErr, ErrBudgetExhausted) {
		return stepErr
	}
	if stepErr != nil && !errors.Is(stepErr, errStop) {
		if step.ContinueOnError {
			return nil
		}
		return apperr.Wrap(stepErr, "automation.errors.step_failed", "step", step.ID)
	}
	return stepErr
}

func (e *Engine) finishStepRow(ctx context.Context, row *RunStep) {
	now := time.Now().UTC()
	row.FinishedAt = &now
	logx.Warn(e.store.RecordStep(context.WithoutCancel(ctx), row), "automation.log.record_step", "step", row.StepID)
}

func (e *Engine) dispatch(ctx context.Context, st *runState, step *Step, row *RunStep) (map[string]any, error) {
	switch step.Kind() {
	case StepCondition:
		return e.stepCondition(st, step, row)
	case StepMCP:
		return e.stepMCP(ctx, st, step)
	case StepHTTP:
		return e.stepHTTP(ctx, st, step)
	case StepScript:
		return e.stepScript(ctx, st, step)
	case StepQuery:
		return e.stepQuery(ctx, st, step)
	case StepTask:
		return e.stepTask(ctx, st, step)
	case StepMention:
		return e.stepMention(ctx, st, step)
	case StepFanout:
		return e.stepFanout(ctx, st, step, row)
	default:
		return nil, apperr.Internal("automation.errors.unknown_step_kind")
	}
}

func evalCondition(src string, ctx map[string]any) (bool, error) {
	expr, err := Compile(src)
	if err != nil {
		return false, err
	}
	return expr.Bool(ctx)
}

func (e *Engine) stepCondition(st *runState, step *Step, row *RunStep) (map[string]any, error) {
	ok, err := evalCondition(step.Condition, st.ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		row.OutputMsg = msgText("automation.runs.condition_held", "condition", step.Condition)
		return map[string]any{"result": true}, nil
	}
	row.OutputMsg = msgText("automation.runs.condition_false", "condition", step.Condition)
	st.stopReason = msgText("automation.runs.stopped_by_condition", "step", step.ID)
	return map[string]any{"result": false}, errStop
}

func (e *Engine) stepFanout(ctx context.Context, st *runState, step *Step, row *RunStep) (map[string]any, error) {
	expr, err := Compile(step.Fanout.Over)
	if err != nil {
		return nil, err
	}
	v, err := expr.Eval(st.ctx)
	if err != nil {
		return nil, err
	}
	items, ok := v.([]any)
	if !ok && v != nil {
		return nil, apperr.Unprocessable("automation.errors.fanout_not_list", "value", v)
	}
	limit := step.Fanout.MaxItems
	truncated := false
	if len(items) > limit {
		items = items[:limit]
		truncated = true
	}
	alias := step.Fanout.As
	if alias == "" {
		alias = "item"
	}
	prev, had := st.ctx[alias]
	defer func() {
		if had {
			st.ctx[alias] = prev
		} else {
			delete(st.ctx, alias)
		}
	}()

	done := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return map[string]any{"items": done}, err
		}
		st.ctx[alias] = item
		if err := e.runSteps(ctx, st, step.Steps); err != nil {
			if errors.Is(err, errStop) {
				st.stopReason = nil
				continue
			}
			return map[string]any{"items": done}, err
		}
		done++
	}
	row.OutputMsg = msgText("automation.runs.fanout_items", "count", done)
	if truncated {
		row.OutputMsg = msgText("automation.runs.fanout_items_capped", "count", done, "max", limit)
	}
	return map[string]any{"items": done, "truncated": truncated}, nil
}

func summarize(result map[string]any) string {
	b, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > MaxStepOutput {
		s = s[:MaxStepOutput] + "…"
	}
	return s
}

func summarizeIn(result map[string]any, msg Msg) Msg {
	if len(msg) == 0 {
		return nil
	}
	field := ""
	for _, k := range []string{"reason", "error"} {
		if _, ok := result[k]; ok {
			field = k
		}
	}
	if field == "" {
		return nil
	}
	out := Msg{}
	for locale, text := range msg {
		copied := make(map[string]any, len(result))
		for k, v := range result {
			copied[k] = v
		}
		copied[field] = text
		out[locale] = summarize(copied)
	}
	return out
}

func renderTemplate(tpl string, ctx map[string]any) (string, error) {
	out, err := Render(tpl, ctx)
	if err != nil {
		return "", apperr.Wrap(err, "automation.errors.template", "template", strings.TrimSpace(tpl))
	}
	return out, nil
}
