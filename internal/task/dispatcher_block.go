package task

import (
	"context"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Why a task is blocked, as a card can show it without parsing the note. Agent names who stopped
// it; the label leads with the agent because columns get renamed and rearranged, agents keep their
// job. Stage stays in the payload for the timeline.
type BlockInfo struct {
	Stage  string `json:"stage,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Cause  string `json:"cause"`
	Reason string `json:"reason,omitempty"`
}

const (
	// The stage's own agent wrote verdict: block (Reason is its code).
	BlockVerdict = "verdict"
	// The stage's auditor said block; Agent is the agent it audited.
	BlockAuditor = "auditor"
	// A send-back past the board's max_kick_backs.
	BlockKickBackLimit = "kick_back_limit"
	// The stage's agent sent the task back a second time for the same single-round reason (Reason).
	BlockKickBackRepeated = "kick_back_repeated"
	// A send-back from a stage that already released the worktree (the branch is merged).
	BlockReleased = "released"
	// A send-back with no earlier stage to land in.
	BlockNoTarget = "no_target"
	// The auditor could not start.
	BlockAuditorFailed = "auditor_failed"
	// The auditor ran and left no verdict.
	BlockAuditorNoVerdict = "auditor_no_verdict"
	// A stage with agent_verdict ended cleanly with no verdict.md that reads; Agent is the stage's.
	BlockVerdictMissing = "verdict_missing"
	// Same, but the run's outputs did not all reach the daemon: the verdict may be what the upload
	// lost, not something the agent failed to write.
	BlockVerdictUndelivered = "verdict_undelivered"
)

// block records why and sets the task blocked. The note that explains it to people is the caller's.
func (d *Dispatcher) block(ctx context.Context, taskID string, stage Stage, b BlockInfo) {
	b.Stage = string(stage)
	b.Agent = strings.TrimPrefix(b.Agent, "agent:")
	logx.Warn(d.store.RecordEvent(ctx, taskID, EventTaskBlocked, "", "octopus", b.Cause, b),
		"task.log.record_task_blocked", "task", taskID)
	d.setStatus(ctx, taskID, StatusBlocked)
}

// The agent behind a send-back request: "agent:x" for a stage verdict, the audited agent for the
// auditor, "" for a person.
func kickBackAgent(req kickBackReq) string {
	if name, ok := strings.CutPrefix(req.Actor, "agent:"); ok {
		return name
	}
	return req.Agent
}
