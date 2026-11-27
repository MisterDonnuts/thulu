package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/evidence"
)

// narrowRunBatch shapes a batch an agent run queues (runMayWriteEvidence let it through only with
// evidence_batch_run named in its grant). The batch records the run's task; against production it
// runs only the cases a person marked safe, whatever the request asked; and it builds a sandbox
// from a branch only on the Sandbox of a project the run's task works on.
func (s *Server) narrowRunBatch(w http.ResponseWriter, r *http.Request, env *evidence.Environment, spec *evidence.BatchSpec) bool {
	scope := auth.APIKeyRunScope(r.Context())
	if !scope.IsRun() {
		return true
	}
	spec.Trigger, spec.TaskID, spec.TriggerRef = evidence.TriggerAgent, scope.TaskID, scope.TaskID
	if s.evidenceEnvIsProduction(r.Context(), env) {
		spec.SafeOnly = true
	}
	if strings.TrimSpace(spec.Branch) != "" && !s.runWorksOnProject(r.Context(), scope, env) {
		writeErrKey(w, r, http.StatusForbidden, "api.evidence.errors.run_branch_scope")
		return false
	}
	return true
}

// evidenceEnvIsProduction: the environment is marked production, or it is the one its project names
// as release_environment. An unreadable project counts as production, so the batch stays safe.
func (s *Server) evidenceEnvIsProduction(ctx context.Context, env *evidence.Environment) bool {
	if env.IsProduction {
		return true
	}
	if env.ProjectID == "" || s.projectStore == nil {
		return false
	}
	p, err := s.projectStore.Get(ctx, env.ProjectID)
	if err != nil || p == nil {
		return true
	}
	rel := strings.TrimSpace(p.ReleaseEnvironment)
	if rel == "" {
		return false
	}
	return strings.EqualFold(env.Name, rel) || strings.EqualFold(env.DisplayName, rel) || env.Name == evidence.DeploySlug(rel)
}

func (s *Server) runWorksOnProject(ctx context.Context, scope auth.RunScope, env *evidence.Environment) bool {
	if env.Kind != evidence.KindSandbox || env.ProjectID == "" || scope.TaskID == "" || s.taskStore == nil {
		return false
	}
	t, err := s.taskStore.GetRow(ctx, scope.TaskID)
	return err == nil && t != nil && slices.Contains(t.ProjectIDs, env.ProjectID)
}
