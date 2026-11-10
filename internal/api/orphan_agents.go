package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// The dispatcher records the start event only after the launch answers; a container younger than
// this is not an orphan yet.
const orphanGrace = 2 * time.Minute

// The current run's container counts as accounted for even without a start event: current_instance
// is written first and a failed RecordEvent is only logged.
// A database error answers false: only call it an orphan when Thulu knows.
func (s *Server) orphanAgent(ctx context.Context, c *infra.Container, now time.Time) bool {
	if c.Kind != infra.KindAgent || !c.IsRunning() || c.Instance == "" || s.taskStore == nil {
		return false
	}
	taskID, ok := task.InstanceTaskID(c.Instance)
	if !ok {
		return false
	}
	if c.TaskID != "" {
		taskID = c.TaskID
	}
	if !c.StartedAt.IsZero() && now.Sub(c.StartedAt) < orphanGrace {
		return false
	}
	t, err := s.taskStore.GetRow(ctx, taskID)
	if err != nil {
		return errors.Is(err, task.ErrNotFound)
	}
	if s.currentRunOf(t, c) {
		return false
	}
	started, err := s.taskStore.RunStartedForInstance(ctx, taskID, c.Instance)
	return err == nil && !started
}

func (s *Server) currentRunOf(t *task.Task, c *infra.Container) bool {
	if t.CurrentInstance != "" && t.CurrentInstance == c.Instance {
		return true
	}
	return t.CurrentRunID != "" && (t.CurrentRunID == c.ID || t.CurrentRunID == infraRunID(c, s.kube != nil))
}

func (s *Server) markOrphans(ctx context.Context, cs []infra.Container) []infra.Container {
	now := time.Now()
	for i := range cs {
		cs[i].Orphan = s.orphanAgent(ctx, &cs[i], now)
	}
	return cs
}

func (s *Server) orphanAgentsOf(ctx context.Context, taskID string) []task.OrphanAgent {
	if s.infra == nil {
		return nil
	}
	now := time.Now()
	var out []task.OrphanAgent
	cs := s.infra.Containers()
	for i := range cs {
		c := &cs[i]
		if c.TaskID != taskID || !s.orphanAgent(ctx, c, now) {
			continue
		}
		out = append(out, task.OrphanAgent{
			ContainerID: c.ID, Name: c.Name, Agent: c.Agent, Stage: c.Stage, StartedAt: c.StartedAt,
		})
	}
	return out
}

// Removed, not stopped: the dispatcher's reconcile replays a stopped task-run container as a
// finished run.
func (s *Server) removeOrphanContainer(ctx context.Context, c *infra.Container) error {
	if s.removeContainerHook != nil {
		return s.removeContainerHook(ctx, c)
	}
	switch {
	case s.kube != nil:
		return s.kube.DeletePod(ctx, c.Name, c.Namespace)
	case s.docker != nil:
		return removeDockerContainer(ctx, s.docker, c.ID)
	}
	return errNoContainerRuntime
}

// Detached from the request so a closed tab doesn't abort a Docker removal halfway; bounded to a
// minute.
func removalContext(req context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(req), time.Minute)
}

type dockerRemover interface {
	ContainerRemove(ctx context.Context, id string, opts container.RemoveOptions) error
	ContainerInspect(ctx context.Context, id string) (types.ContainerJSON, error)
}

// Docker can return an error from a force removal it still completes (kill racing the exit event,
// removal already under way), so check the state before failing.
func removeDockerContainer(ctx context.Context, d dockerRemover, id string) error {
	err := d.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
	if err == nil || errdefs.IsNotFound(err) {
		return nil
	}
	info, ierr := d.ContainerInspect(ctx, id)
	if errdefs.IsNotFound(ierr) {
		return nil
	}
	if ierr == nil && info.ContainerJSONBase != nil && info.State != nil && (info.State.Dead || info.State.Status == "removing") {
		return nil
	}
	return err
}

var errNoContainerRuntime = errors.New("no container runtime") //i18n:ignore internal sentinel, answered as a keyed 409

func (s *Server) handleRemoveOrphanAgent(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.infra == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.infra.errors.unavailable")
		return
	}
	id := r.PathValue("containerId")
	var c *infra.Container
	cs := s.infra.Containers()
	for i := range cs {
		if cs[i].ID == id && cs[i].TaskID == t.ID {
			c = &cs[i]
			break
		}
	}
	if c == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.infra.errors.container_not_found", "id", id)
		return
	}
	if !s.orphanAgent(r.Context(), c, time.Now()) {
		writeErrKey(w, r, http.StatusConflict, "api.infra.errors.not_orphan", "name", c.Name)
		return
	}
	ctx, cancel := removalContext(r.Context())
	defer cancel()
	if err := s.removeOrphanContainer(ctx, c); err != nil {
		if errors.Is(err, errNoContainerRuntime) {
			writeErrKey(w, r, http.StatusConflict, "api.infra.errors.not_stoppable", "kind", string(c.Kind))
			return
		}
		log.Printf("[infra] %s", i18n.Log("api.infra.log.orphan_remove_failed", "container", c.Name, "task", t.ID, "error", err))
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.infra.Forget(c.ID)
	log.Printf("[infra] %s", i18n.Log("api.infra.log.orphan_removed", "container", c.Name, "task", t.ID))
	w.WriteHeader(http.StatusNoContent)
}
