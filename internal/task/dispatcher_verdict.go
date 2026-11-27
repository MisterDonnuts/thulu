package task

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// verdict.md reasons; anything else reads as "other".
var verdictReasons = map[string]bool{
	"gates_failed": true, "review_blocking": true, "qa_failed": true, "mutants_survived": true,
	"spec_test_conflict": true, "nothing_to_change": true, "gate_config_changed": true, "other": true,
	// Production readiness.
	"branch_behind": true, "commit_rules": true, "forbidden_files": true, "spec_mismatch": true,
	"evidence_missing": true, "review_shallow": true, "reports_inconsistent": true, "nits": true,
	"repeated_kick_back": true, "needs_decision": true,
}

// Send-back reasons one round must fix: rebase, rewrite the commits, drop a file, fix a nit, run the
// missing cases. When the same stage sends the task back for one of them a second time since the
// last reset, the round did not work and a person decides: the task blocks instead of looping
// until the board's cap. Failing gates, tests or review findings keep only the cap, since a second
// round may fail for new reasons.
var singleRoundReasons = map[string]bool{
	"branch_behind": true, "commit_rules": true, "forbidden_files": true, "spec_mismatch": true,
	"evidence_missing": true, "review_shallow": true, "reports_inconsistent": true, "nits": true,
}

func (v stageVerdict) reasonCode() string {
	if r := strings.TrimSpace(v.Reason); verdictReasons[r] {
		return r
	}
	return "other"
}

// Quoted only in the notes of a send-back Thulu refused (repeated, released), where a person
// decides; the target stage of a send-back reads the body in its brief. The contract asks for 3-15
// lines, so more is cut.
const maxVerdictBodyLines = 15

func quotedVerdictBody(body string) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) > maxVerdictBodyLines {
		lines = append(lines[:maxVerdictBodyLines], "…")
	}
	if q := quoteNote(strings.Join(lines, "\n")); q != "" {
		return "\n\n" + q
	}
	return ""
}

// Disk first (Docker), then the report the pod uploaded to /tasks/{id}/stage-verdict for the same
// instance. Only continue, kick_back and block are verdicts a stage agent may give.
func (d *Dispatcher) agentVerdict(ctx context.Context, taskID, instanceID, outputDir string) stageVerdict {
	v := d.readVerdict(ctx, taskID, instanceID, outputDir, "verdict.md", ReportKindStageVerdict)
	switch v.Verdict {
	case verdictContinue, verdictKickBack, verdictBlock:
		v.Source = verdictFromAgent
		return v
	}
	return stageVerdict{}
}

// The verdict a stage with agent_verdict gave on a clean exit, when it ends the run without the
// auditor (kick_back or block). A missing or malformed verdict.md blocks the task: in a stage whose
// agent decides, a clean exit with no decision is not a pass. Testing that wrote "kick_back" only in
// its report and went on to human review is why.
func (d *Dispatcher) takeStageVerdict(ctx context.Context, t *Task, stage Stage, run auditedRun) (stageVerdict, bool) {
	if t == nil || run.Exit != 0 || run.NoAdvance {
		return stageVerdict{}, false
	}
	info, _ := d.stagesFor(t).Get(stage)
	if !info.AgentVerdict {
		if _, err := os.Stat(filepath.Join(run.OutputDir, "verdict.md")); err == nil && run.OutputDir != "" {
			log.Printf("[task] %s", i18n.Log("task.log.verdict_ignored", "task", t.ID, "stage", stage))
		}
		return stageVerdict{}, false
	}
	v := d.agentVerdict(ctx, t.ID, run.Instance, run.OutputDir)
	if v.Verdict == "" {
		return stageVerdict{Verdict: verdictBlock, Source: verdictFromAgent, Agent: run.Agent, missing: true,
			undelivered: d.store.OutputsUndelivered(ctx, t.ID, run.Instance)}, true
	}
	v.Agent, v.instance = run.Agent, run.Instance
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventStageVerdict, run.Instance, "agent:"+run.Agent,
		fmt.Sprintf("%s · %s", stage, v.Verdict),
		map[string]string{"verdict": v.Verdict, "kick_back_to": v.KickBackTo, "reason": v.reasonCode(),
			"stage": string(stage), "agent": run.Agent}),
		"task.log.record_stage_verdict", "task", t.ID)
	if v.Verdict == verdictContinue {
		// Not taken: the stage goes on to its auditor, which reads what the agent said.
		return v, false
	}
	return v, true
}

// A run that left no verdict blocks the task. When its outputs did not all reach the daemon, the
// note says the verdict was lost on the way instead of blaming the agent for not writing it.
func (d *Dispatcher) blockMissingVerdict(ctx context.Context, t *Task, taskID string, stage Stage, v stageVerdict) {
	key, cause := "task.comments.verdict_missing", BlockVerdictMissing
	if v.undelivered {
		key, cause = "task.comments.verdict_undelivered", BlockVerdictUndelivered
	}
	d.systemComment(ctx, taskID, NoteOf(key, "agent", v.Agent, "stage", d.stageNote(t, stage)))
	d.block(ctx, taskID, stage, BlockInfo{Cause: cause, Agent: v.Agent})
}

// Docker: the daemon stores verdict.md as a report unless the container already uploaded it.
func (d *Dispatcher) postVerdictArtifact(ctx context.Context, taskID, agentName, instanceID, outputDir string) {
	if _, err := os.Stat(filepath.Join(outputDir, "verdict.posted")); err == nil {
		return
	}
	if body, ok := readFile(outputDir, "verdict.md"); ok {
		_, err := d.store.AddReport(ctx, taskID, SourceAgent, agentName, ReportKindStageVerdict, "", body, instanceID)
		logx.Warn(err, "task.log.add_stage_verdict", "task", taskID)
	}
}

// The stage-verdict route checks this against the run key's scope so a verdict lands only on the
// run that key was minted for.
func IsStageInstanceFor(instanceID, taskID, stage string) bool {
	gotTask, gotStage, ok := parseTaskInstanceID(instanceID)
	return ok && gotTask == taskID && string(gotStage) == stage
}
