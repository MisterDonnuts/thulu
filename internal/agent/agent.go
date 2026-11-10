package agent

import "time"

type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusExited  Status = "exited"
	StatusUnknown Status = "unknown"
)

type Agent struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Project  string `json:"project"`
	Instance string `json:"instance"`
	Engine   string `json:"engine,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
	// Empty on runs launched before thulu.workspace_id existed; the API falls back to the task's
	// workspace.
	WorkspaceID string     `json:"workspace_id,omitempty"`
	ProjectID   string     `json:"project_id,omitempty"`
	Stage       string     `json:"stage,omitempty"`
	Image       string     `json:"image"`
	Status      Status     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	StopReason  string     `json:"stop_reason,omitempty"`
	OutputDir   string     `json:"output_dir,omitempty"`
}

func (a *Agent) Uptime() time.Duration {
	if a.Status != StatusRunning {
		if a.FinishedAt != nil {
			return a.FinishedAt.Sub(a.StartedAt)
		}
		return 0
	}
	return time.Since(a.StartedAt)
}

const (
	LabelManaged   = "thulu.agent"
	LabelType      = "thulu.type"
	LabelProject   = "thulu.project"
	LabelInstance  = "thulu.instance"
	LabelEngine    = "thulu.engine"
	LabelTask      = "thulu.task"
	LabelProjectID = "thulu.project_id"
	LabelStage     = "thulu.stage"

	// Chat containers (internal/chatruntime) get thulu.agent="false" via
	// LaunchParams.LabelOverrides so the watcher, registry and board ignore them.
	LabelChat   = "thulu.chat"
	LabelChatID = "thulu.chat_id"

	LabelWorkspaceID = "thulu.workspace_id"
)
