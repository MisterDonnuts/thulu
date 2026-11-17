package api

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// How long SteerRun waits for the machine to say what it did with a message. Past it the dispatcher
// queues the message for a continuation, as when Docker's exec fails.
const steerAckWait = 10 * time.Second

// steerHub holds the messages sent to running runner jobs until the job's machine takes them, and
// hands each sender the machine's answer. In memory: the chart keeps one daemon replica, and the
// dispatcher's steering arms live in memory too.
type steerHub struct {
	mu   sync.Mutex
	seq  uint64
	jobs map[string]*steerJob
	// Closed and replaced on every new line, so a waiting poll looks again.
	wake chan struct{}
}

type steerJob struct {
	pending []*steerLine
	taken   map[string]*steerLine
}

type steerLine struct {
	id   string
	line string
	// Buffered: whoever removes the line from the hub sends its result exactly once.
	result chan string
}

func newSteerHub() *steerHub {
	return &steerHub{jobs: map[string]*steerJob{}, wake: make(chan struct{})}
}

func (h *steerHub) push(jobID, line string) *steerLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	l := &steerLine{id: strconv.FormatUint(h.seq, 10), line: line, result: make(chan string, 1)}
	j := h.jobs[jobID]
	if j == nil {
		j = &steerJob{taken: map[string]*steerLine{}}
		h.jobs[jobID] = j
	}
	j.pending = append(j.pending, l)
	close(h.wake)
	h.wake = make(chan struct{})
	return l
}

// take waits until ctx ends for lines sent to jobID and marks the ones it returns as taken.
func (h *steerHub) take(ctx context.Context, jobID string) []runner.SteerMessage {
	for {
		h.mu.Lock()
		var out []runner.SteerMessage
		if j := h.jobs[jobID]; j != nil {
			for _, l := range j.pending {
				j.taken[l.id] = l
				out = append(out, runner.SteerMessage{ID: l.id, Line: l.line})
			}
			j.pending = nil
		}
		wake := h.wake
		h.mu.Unlock()
		if len(out) > 0 {
			return out
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		}
	}
}

func (h *steerHub) ack(jobID string, acks []runner.SteerAck) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[jobID]
	if j == nil {
		return
	}
	for _, a := range acks {
		if l := j.taken[a.ID]; l != nil {
			delete(j.taken, a.ID)
			l.result <- a.Result
		}
	}
	h.pruneLocked(jobID)
}

// expire withdraws a line nobody answered for, so a late poll cannot hand it out. False when the
// line already has its result.
func (h *steerHub) expire(jobID string, l *steerLine) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[jobID]
	if j == nil {
		return false
	}
	found := j.taken[l.id] != nil
	delete(j.taken, l.id)
	for i, p := range j.pending {
		if p == l {
			j.pending = append(j.pending[:i], j.pending[i+1:]...)
			found = true
			break
		}
	}
	h.pruneLocked(jobID)
	return found
}

// end answers every line still waiting on a job that stopped: the agent no longer reads its queue.
func (h *steerHub) end(jobID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[jobID]
	if j == nil {
		return
	}
	for _, l := range j.pending {
		l.result <- runner.SteerSealed
	}
	for _, l := range j.taken {
		l.result <- runner.SteerSealed
	}
	delete(h.jobs, jobID)
}

func (h *steerHub) pruneLocked(jobID string) {
	if j := h.jobs[jobID]; j != nil && len(j.pending) == 0 && len(j.taken) == 0 {
		delete(h.jobs, jobID)
	}
}

var steerResultKeys = map[string]string{
	runner.SteerSealed: "api.runners.errors.steer_sealed",
	runner.SteerLocked: "api.runners.errors.steer_locked",
}

func steerResultErr(result string) error {
	if result == runner.SteerAppended {
		return nil
	}
	if key, ok := steerResultKeys[result]; ok {
		return apperr.Precondition(key)
	}
	return apperr.Precondition("api.runners.errors.steer_failed", "result", result)
}

// SteerRun hands a message to the machine running the job and returns once the machine wrote it to
// the agent's queue. Any other outcome is an error, and the dispatcher queues the message.
func (l *runnerLauncher) SteerRun(ctx context.Context, runID string, payload []byte) error {
	jobID := runner.JobIDFromRun(runID)
	// A queued job is alive but has no agent reading yet.
	if j, err := l.store.GetJob(ctx, jobID); err != nil || j.State != runner.StateRunning {
		return apperr.Precondition("api.runners.errors.steer_not_running")
	}
	if len(payload) > runner.MaxSteerLine {
		return apperr.Invalid("api.runners.errors.steer_too_large", "max", runner.MaxSteerLine>>10)
	}
	line := l.steer.push(jobID, string(payload))
	timer := time.NewTimer(steerAckWait)
	defer timer.Stop()
	select {
	case res := <-line.result:
		return steerResultErr(res)
	case <-ctx.Done():
	case <-timer.C:
	}
	if l.steer.expire(jobID, line) {
		return apperr.Precondition("api.runners.errors.steer_timeout")
	}
	// The answer arrived while the wait ended.
	return steerResultErr(<-line.result)
}

// handleRunnerSteer applies the acks the runner brings and long-polls for the job's next messages.
func (s *Server) handleRunnerSteer(w http.ResponseWriter, r *http.Request) {
	var req runner.SteerRequest
	if !decodeBody(w, r, &req) {
		return
	}
	rn, jobID := callingRunner(r), r.PathValue("id")
	hub := s.runnerRoute.runner.steer
	if _, err := s.runnerStore.Held(r.Context(), rn.ID, jobID); err != nil {
		// A message appended right before the job ended still reached the agent.
		if j, gerr := s.runnerStore.GetJob(r.Context(), jobID); gerr == nil && j.RunnerID == rn.ID && j.Done() {
			hub.ack(jobID, req.Acks)
			writeJSON(w, http.StatusOK, runner.SteerResponse{Done: true})
			return
		}
		writeError(w, r, apperr.Status(err), err)
		return
	}
	hub.ack(jobID, req.Acks)
	ctx, cancel := context.WithTimeout(r.Context(), runner.ClaimWait)
	defer cancel()
	writeJSON(w, http.StatusOK, runner.SteerResponse{Messages: hub.take(ctx, jobID)})
}
