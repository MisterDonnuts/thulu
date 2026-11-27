package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// openStopThread posts the stop a stage's agent raised with verdict block: a comment of kind block,
// written as the agent, whose body is what the agent asked of a person (its ask, else the first
// sentence of its verdict). The full report stays in Reports; meta points at it. A stop still open
// from an earlier run is resolved first: the new one replaces it.
func (d *Dispatcher) openStopThread(ctx context.Context, taskID string, v stageVerdict) {
	text, cut := stopAsk(string(v.Ask), v.Body)
	reportID := d.store.verdictReportID(ctx, taskID, v.instance)
	runID := d.store.runContainerOf(ctx, taskID, v.instance)
	if cut {
		msg := i18n.Text("task.comments.stop_ask_cut", "agent", v.Agent, "max", maxStopAskRunes)
		log.Printf("[task] %s", i18n.Log("task.log.stop_ask_cut", "task", taskID, "agent", v.Agent,
			"instance", v.instance, "max", maxStopAskRunes))
		logx.Warn(d.store.RecordEvent(ctx, taskID, EventRunnerWarning, runID, "octopus", msg,
			map[string]string{"run_id": runID, "instance_id": v.instance, "message": msg}),
			"task.log.record_runner_warning", "task", taskID)
	}
	d.store.ResolveStopThreads(ctx, taskID, "octopus")
	in := NewComment{
		TaskID: taskID, Source: SourceAgent, Author: v.Agent, Body: text, Kind: CommentBlock,
		Meta: &CommentMeta{ReportID: reportID, RunID: runID},
	}
	if text == "" {
		n := NoteOf("task.comments.stop_no_ask", "agent", v.Agent)
		in.Note = &n
	}
	_, err := d.store.PostComment(ctx, in)
	logx.Warn(err, "task.log.open_stop_thread", "task", taskID)
}

// verdictReportID is the newest stage_verdict report the run instance left; "" when none.
func (s *Store) verdictReportID(ctx context.Context, taskID, instance string) string {
	if instance == "" {
		return ""
	}
	var id string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id FROM task_reports WHERE task_id = ? AND kind = ? AND instance_id = ?
		  ORDER BY created_at DESC, id DESC LIMIT 1`), taskID, ReportKindStageVerdict, instance).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logx.Warn(err, "task.log.open_stop_thread", "task", taskID)
	}
	return id
}

// runContainerOf is the container (the run id the task view uses) of the run instance, read from
// its agent_run_finished event; "" when the event is missing.
func (s *Store) runContainerOf(ctx context.Context, taskID, instance string) string {
	if instance == "" {
		return ""
	}
	var raw string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT payload FROM workflow_events WHERE task_id = ? AND kind = ? AND ref_id = ?
		  ORDER BY created_at DESC LIMIT 1`), taskID, EventAgentRunFinished, instance).Scan(&raw)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logx.Warn(err, "task.log.open_stop_thread", "task", taskID)
		}
		return ""
	}
	var p struct {
		ContainerID string `json:"container_id"`
	}
	logx.Warn(json.Unmarshal([]byte(raw), &p), "task.log.open_stop_thread", "task", taskID)
	return p.ContainerID
}
