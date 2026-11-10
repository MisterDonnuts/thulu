package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/syncx"
)

type AcquireResult struct {
	Outcome       AcquireOutcome
	ContainerID   string
	HostPorts     map[int]int
	QueuePosition int
}

type Manager struct {
	store    *Store
	settings *settings.Store
	docker   Runtime
	queue    *Queue

	publicHost   string
	publicScheme string
	memoryBytes  int64
	dataVolume   string
	dataRoot     string
	appPorts     []int

	// Per-task lock so concurrent exec/preview calls for one task don't race on create; the queue
	// only serializes the global cap.
	taskMu syncx.KeyedMutex
	// The idle sweep skips a task with running commands (limits.go).
	execs execTracker
	dead  deadSuspects

	// initMu cannot be taskMu: every launch step is an Exec, and Exec takes taskMu itself.
	inits               initRegistry
	initMu              syncx.KeyedMutex
	meter               cacheMeter
	roomWait, roomRetry time.Duration
}

const (
	defaultRoomWait  = 3 * time.Minute
	defaultRoomRetry = 5 * time.Second
)

type ManagerConfig struct {
	PublicHost    string
	PublicScheme  string
	OnSlotGranted SlotGrantedCallback
	MemoryBytes   int64
	DataVolume    string
	DataRoot      string
	AppPorts      []int
}

// 6 GB fits Angular/Next/Vite builds plus a dev server on a typical Docker Desktop allocation.
const DefaultSandboxMemoryBytes int64 = 6 * 1024 * 1024 * 1024

func NewManager(store *Store, sett *settings.Store, dock Runtime, cfg ManagerConfig) *Manager {
	if cfg.PublicHost == "" {
		cfg.PublicHost = "localhost"
	}
	if cfg.PublicScheme == "" {
		cfg.PublicScheme = "http"
	}
	mem := cfg.MemoryBytes
	switch {
	case mem == 0:
		mem = DefaultSandboxMemoryBytes
	case mem < 0:
		mem = 0
	}
	m := &Manager{
		store:        store,
		settings:     sett,
		docker:       dock,
		publicHost:   cfg.PublicHost,
		publicScheme: cfg.PublicScheme,
		memoryBytes:  mem,
		dataVolume:   cfg.DataVolume,
		dataRoot:     cfg.DataRoot,
		appPorts:     slices.Clone(cfg.AppPorts),
		roomWait:     defaultRoomWait,
		roomRetry:    defaultRoomRetry,
	}
	if len(m.appPorts) == 0 {
		m.appPorts = slices.Clone(DefaultExposedPorts)
	}
	m.queue = NewQueue(store, sett, cfg.OnSlotGranted)
	return m
}

func (m *Manager) AppPorts() []int { return slices.Clone(m.appPorts) }

func (m *Manager) Queue() *Queue { return m.queue }

func (m *Manager) Acquire(ctx context.Context, taskID, stage string, opts AcquireDockerOpts) (*AcquireResult, error) {
	if taskID == "" {
		return nil, apperr.Invalid("sandbox.errors.task_required")
	}
	mu := m.taskLock(taskID)
	mu.Lock()
	defer mu.Unlock()

	// Retries when an existing row points to a vanished container; recursing instead would deadlock
	// on the per-task mutex.
	for attempt := 0; attempt < 3; attempt++ {
		qres, err := m.queue.Acquire(ctx, taskID, stage)
		if err != nil {
			return nil, err
		}
		switch qres.Outcome {
		case AcquireExisting:
			res, retry, err := m.acquireExisting(ctx, taskID)
			if retry {
				continue
			}
			return res, err
		case AcquireQueued:
			return &AcquireResult{Outcome: AcquireQueued, QueuePosition: qres.QueuePosition}, nil
		case AcquireGranted:
			return m.acquireGranted(ctx, taskID, opts)
		}
		return nil, apperr.Internal("sandbox.errors.unknown_acquire_outcome", "outcome", qres.Outcome)
	}
	return nil, apperr.Unavailable("sandbox.errors.acquire_too_many_retries")
}

func (m *Manager) acquireExisting(ctx context.Context, taskID string) (result *AcquireResult, retry bool, err error) {
	sb, err := m.store.Get(ctx, taskID)
	if err != nil {
		return nil, false, apperr.Wrap(err, "sandbox.errors.get_existing")
	}
	if sb.Status == StatusStarting {
		if waitErr := m.waitReady(ctx, taskID, 30*time.Second); waitErr != nil {
			return nil, false, waitErr
		}
		if sb, err = m.store.Get(ctx, taskID); err != nil {
			return nil, false, err
		}
	}
	if !m.docker.Alive(ctx, sb.ContainerID) {
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.container_gone", "task", taskID, "container", sb.ContainerID))
		logx.Warn(m.docker.Remove(ctx, sb.ContainerID), "sandbox.log.reap_container", "container", sb.ContainerID)
		logx.Warn(m.queue.Release(ctx, taskID), "sandbox.log.release_slot", "task", taskID)
		return nil, true, nil
	}
	return &AcquireResult{
		Outcome:     AcquireExisting,
		ContainerID: sb.ContainerID,
		HostPorts:   sb.HostPorts,
	}, false, nil
}

// Holds the slot while waiting for quota room: answering "queued" would make the agent wait for a
// relaunch nobody schedules (only the slot queue relaunches).
func (m *Manager) createWithRoom(ctx context.Context, taskID string, opts CreateOpts) (*CreateResult, error) {
	deadline := time.Now().Add(m.roomWait)
	logged := false
	for {
		res, err := m.docker.Create(ctx, opts)
		if err == nil || !kubeapi.IsQuotaFull(err) || time.Now().Add(m.roomRetry).After(deadline) {
			return res, err
		}
		if !logged {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.quota_full", "task", taskID, "err", apperr.Text(err)))
			logged = true
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(m.roomRetry):
		}
	}
}

func (m *Manager) acquireGranted(ctx context.Context, taskID string, opts AcquireDockerOpts) (*AcquireResult, error) {
	res, err := m.createWithRoom(ctx, taskID, CreateOpts{
		TaskID:        taskID,
		WorkspaceID:   opts.WorkspaceID,
		WorkspacePath: opts.WorkspacePath,
		ProjectsPaths: opts.ProjectsPaths,
		ExposePorts:   m.appPorts,
		MemoryBytes:   m.memoryBytes,
		DataVolume:    m.dataVolume,
		DataRoot:      m.dataRoot,

		SnapshotsWritable: opts.SnapshotsWritable,
	})
	if err != nil {
		logx.Warn(m.store.SetStatus(ctx, taskID, StatusFailed), "sandbox.log.mark_failed", "task", taskID)
		logx.Warn(m.queue.Release(ctx, taskID), "sandbox.log.release_slot", "task", taskID)
		return nil, apperr.Wrap(err, "sandbox.errors.docker_create")
	}
	now := time.Now().UTC()
	sb := &Sandbox{
		TaskID:         taskID,
		ContainerID:    res.ContainerID,
		Image:          m.docker.Image(),
		Status:         StatusReady,
		HostPorts:      res.HostPorts,
		CreatedAt:      now,
		LastActivityAt: now,
	}
	if err := m.store.Upsert(ctx, sb); err != nil {
		logx.Warn(m.docker.Remove(ctx, res.ContainerID), "sandbox.log.cleanup_container", "container", res.ContainerID)
		logx.Warn(m.queue.Release(ctx, taskID), "sandbox.log.release_slot", "task", taskID)
		return nil, apperr.Wrap(err, "sandbox.errors.store_upsert")
	}
	log.Printf("[sandbox] %s", i18n.Log("sandbox.log.ready", "task", taskID, "container", res.ContainerID, "ports", res.HostPorts))
	return &AcquireResult{
		Outcome:     AcquireGranted,
		ContainerID: res.ContainerID,
		HostPorts:   res.HostPorts,
	}, nil
}

type AcquireDockerOpts struct {
	WorkspaceID   string
	WorkspacePath string
	ProjectsPaths map[string]string
	// Only a prewarm may set this: snapshots must come from the default branch, never from a task's
	// branch.
	SnapshotsWritable bool
}

type hostMounter interface{ MountsHostPaths() bool }

func (m *Manager) MountsHostPaths() bool {
	hm, ok := m.docker.(hostMounter)
	return ok && hm.MountsHostPaths()
}

func (m *Manager) Exec(ctx context.Context, taskID, stage string, dockerOpts AcquireDockerOpts, exec ExecOpts) (*ExecResult, *AcquireResult, error) {
	// Mark busy before Acquire so the idle sweep can't evict between handing back the container and
	// the command starting.
	m.execs.begin(taskID)
	defer m.execs.end(taskID)
	ar, err := m.Acquire(ctx, taskID, stage, dockerOpts)
	if err != nil {
		return nil, nil, err
	}
	if ar.Outcome == AcquireQueued {
		return nil, ar, nil
	}
	exec.TimeoutMs = clampExecTimeout(exec.TimeoutMs, m.maxExecTimeoutMs(ctx))
	res, err := m.docker.Exec(ctx, ar.ContainerID, exec)
	if err != nil {
		return nil, ar, err
	}
	res.TimeoutMs = exec.TimeoutMs
	if err := m.store.Touch(ctx, taskID); err != nil {
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.touch_failed", "task", taskID, "err", err))
	}
	return res, ar, nil
}

// URL is for sibling containers: localhost there is the container itself, and published ports bind
// 127.0.0.1, unreachable from containers.
// HumanURL is for a person on the host; empty on Kubernetes, where nothing publishes sandbox ports
// outside the cluster.
type PreviewInfo struct {
	URL      string `json:"url"`
	HumanURL string `json:"human_url"`
	Port     int    `json:"port"`
	Note     string `json:"note,omitempty"`
}

func (m *Manager) PreviewURL(ctx context.Context, taskID string, containerPort int) (PreviewInfo, error) {
	sb, err := m.store.Get(ctx, taskID)
	if err != nil {
		return PreviewInfo{}, err
	}
	hostPort, ok := sb.HostPorts[containerPort]
	if !ok {
		return PreviewInfo{}, apperr.Invalid("sandbox.errors.port_not_exposed",
			"port", containerPort, "available", portList(slices.Sorted(maps.Keys(sb.HostPorts))))
	}
	if ca, ok := m.docker.(ClusterAddresser); ok {
		u, ok := ca.ClusterAddress(ctx, sb.ContainerID, containerPort)
		if !ok {
			return PreviewInfo{}, apperr.NotFound("sandbox.errors.no_cluster_address", "container", sb.ContainerID)
		}
		return PreviewInfo{URL: u, Port: containerPort, Note: i18n.Text("sandbox.tools.preview_cluster_note")}, nil
	}
	info := PreviewInfo{
		URL:      fmt.Sprintf("%s://host.docker.internal:%d", m.publicScheme, hostPort),
		HumanURL: fmt.Sprintf("%s://%s:%d", m.publicScheme, m.publicHost, hostPort),
		Port:     containerPort,
	}
	if pa, ok := m.docker.(PeerAddresser); ok && sb.ContainerID != "" {
		if u, ok := pa.PeerAddress(ctx, sb.ContainerID, containerPort); ok {
			info.URL = u
		}
	}
	return info, nil
}

func (m *Manager) HumanURLs(sb *Sandbox) map[int]string {
	if !m.PublishesPorts() || len(sb.HostPorts) == 0 {
		return nil
	}
	out := make(map[int]string, len(sb.HostPorts))
	for port, hostPort := range sb.HostPorts {
		out[port] = fmt.Sprintf("%s://%s:%d", m.publicScheme, m.publicHost, hostPort)
	}
	return out
}

func (m *Manager) PublishesPorts() bool {
	_, clusterOnly := m.docker.(ClusterAddresser)
	return !clusterOnly
}

func (m *Manager) Sandbox(ctx context.Context, ownerID string) (*Sandbox, error) {
	return m.store.Get(ctx, ownerID)
}

type PeerAddresser interface {
	PeerAddress(ctx context.Context, containerID string, port int) (string, bool)
}

type ClusterAddresser interface {
	ClusterAddress(ctx context.Context, containerID string, port int) (string, bool)
}

func (m *Manager) Release(ctx context.Context, taskID string) error {
	sb, err := m.store.Get(ctx, taskID)
	if errors.Is(err, ErrNotFound) {
		// Storage can exist without a sandbox: an agent pod creates the worktree claim before the
		// task acquires one.
		logx.Warn(m.queue.Cancel(ctx, taskID), "sandbox.log.cancel_queue_entry", "task", taskID)
		m.releaseStorage(ctx, taskID)
		return nil
	}
	if err != nil {
		return err
	}
	m.inits.drop(taskID)
	if sb.ContainerID != "" {
		if err := m.docker.Remove(ctx, sb.ContainerID); err != nil {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.docker_remove_failed", "container", sb.ContainerID, "err", err))
		}
	}
	m.releaseStorage(ctx, taskID)
	return m.queue.Release(ctx, taskID)
}

func (m *Manager) releaseStorage(ctx context.Context, taskID string) {
	if sr, ok := m.docker.(StorageReleaser); ok {
		logx.Warn(sr.ReleaseStorage(ctx, taskID), "sandbox.log.release_storage", "task", taskID)
	}
}

func (m *Manager) SweepTerminated(ctx context.Context, olderThan time.Duration) int {
	sw, ok := m.docker.(TerminatedSweeper)
	if !ok {
		return 0
	}
	n, err := sw.SweepTerminated(ctx, olderThan)
	logx.Warn(err, "sandbox.log.sweep_terminated")
	return n
}

func (m *Manager) Destroy(ctx context.Context, taskID string) error {
	if err := m.Release(ctx, taskID); err != nil {
		return err
	}
	return m.store.Delete(ctx, taskID)
}

func (m *Manager) ReapEvicted(ctx context.Context) {
	all, err := m.store.ListAll(ctx)
	if err != nil {
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.reap_list_failed", "err", err))
		return
	}
	for _, sb := range all {
		if sb.Status != StatusEvicted || sb.ContainerID == "" {
			continue
		}
		if !m.docker.Alive(ctx, sb.ContainerID) {
			continue
		}
		if err := m.docker.Remove(ctx, sb.ContainerID); err != nil {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.reap_remove_failed", "container", sb.ContainerID, "err", err))
			continue
		}
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.reaped_evicted", "container", sb.ContainerID, "task", sb.TaskID))
	}
}

func (m *Manager) GCOnce(ctx context.Context) {
	m.ReapEvicted(ctx)
	m.ReapDead(ctx)
	m.ReapIdle(ctx)
}

func (m *Manager) StartGC(ctx context.Context, interval time.Duration) {
	m.queue.StartGC(ctx)
	go syncx.TickUntil(ctx, interval, m.GCOnce)
}

func (m *Manager) taskLock(taskID string) *sync.Mutex { return m.taskMu.Get(taskID) }

func (m *Manager) waitReady(ctx context.Context, taskID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sb, err := m.store.Get(ctx, taskID)
		if err != nil {
			return err
		}
		if sb.Status == StatusReady {
			return nil
		}
		if sb.Status == StatusFailed {
			return apperr.Conflict("sandbox.errors.sandbox_failed", "task", taskID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return apperr.Timeout("sandbox.errors.wait_ready_timeout", "task", taskID)
}
