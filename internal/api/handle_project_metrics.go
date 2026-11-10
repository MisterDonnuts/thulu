package api

import (
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type projectMetrics struct {
	ProjectID string           `json:"project_id"`
	From      time.Time        `json:"from"`
	To        time.Time        `json:"to"`
	Flow      task.FlowMetrics `json:"flow"`
	Agent     agentRollup      `json:"agent"`
}

type agentRollup struct {
	usage.Efficiency

	TasksWithRuns   int     `json:"tasks_with_runs"`
	CostPerTask     float64 `json:"cost_per_task"`
	TokensPerTask   float64 `json:"tokens_per_task"`
	SessionsPerTask float64 `json:"sessions_per_task"`

	CostPerCompletedTask float64 `json:"cost_per_completed_task"`

	ComputeTimePerTask task.Dist `json:"compute_time_per_task"`
	WallTimePerTask    task.Dist `json:"wall_time_per_task"`
}

// The path's project overrides any ?project_id= so the response never describes a project the
// caller was not authorized for.
func (s *Server) handleProjectMetrics(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}

	f := s.parseFilter(r)
	f.ProjectID = proj.ID
	f = f.Defaults()

	flow, err := s.taskStore.ProjectFlow(r.Context(), proj.ID, f.From, f.To)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	agent, err := s.agentRollupFor(r, f)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if flow.TasksCompleted > 0 {
		agent.CostPerCompletedTask = agent.CostUSD / float64(flow.TasksCompleted)
	}

	writeJSON(w, http.StatusOK, projectMetrics{
		ProjectID: proj.ID,
		From:      f.From,
		To:        f.To,
		Flow:      flow,
		Agent:     agent,
	})
}

// Uses all tasks in the window, not TopTasks, which only covers the expensive tail and would bias
// averages upward.
func (s *Server) agentRollupFor(r *http.Request, f usage.Filter) (agentRollup, error) {
	eff, err := s.usageStore.Efficiency(r.Context(), f)
	if err != nil {
		return agentRollup{}, err
	}
	rows, err := s.usageStore.AllTasks(r.Context(), f)
	if err != nil {
		return agentRollup{}, err
	}

	out := agentRollup{Efficiency: eff, TasksWithRuns: len(rows)}
	if len(rows) == 0 {
		return out, nil
	}

	compute := make([]int64, 0, len(rows))
	wall := make([]int64, 0, len(rows))
	for i := range rows {
		t := &rows[i]
		compute = append(compute, t.TotalDurationMs)
		if !t.FirstStart.IsZero() && t.LastFinish.After(t.FirstStart) {
			wall = append(wall, t.LastFinish.Sub(t.FirstStart).Milliseconds())
		}
	}
	out.ComputeTimePerTask = task.Summarize(compute)
	out.WallTimePerTask = task.Summarize(wall)

	n := float64(len(rows))
	out.CostPerTask = eff.CostUSD / n
	out.TokensPerTask = float64(eff.TotalTokens) / n
	out.SessionsPerTask = float64(eff.Sessions) / n
	return out, nil
}
