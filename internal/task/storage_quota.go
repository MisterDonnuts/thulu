package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// 0 or less means unlimited.
type StorageQuotaFunc func(workspaceID string) int64

// Behind a pointer so Store stays copyable by value in tests.
type storageQuota struct {
	quota StorageQuotaFunc
	extra func(ctx context.Context, workspaceID string) (int64, error)
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (q *storageQuota) lock(workspaceID string) *sync.Mutex {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.locks == nil {
		q.locks = map[string]*sync.Mutex{}
	}
	m, ok := q.locks[workspaceID]
	if !ok {
		m = &sync.Mutex{}
		q.locks[workspaceID] = m
	}
	return m
}

func (s *Store) WithStorageQuota(q StorageQuotaFunc) *Store {
	if s.storage == nil {
		s.storage = &storageQuota{}
	}
	s.storage.quota = q
	return s
}

func (s *Store) WithExtraStorage(extra func(ctx context.Context, workspaceID string) (int64, error)) *Store {
	if s.storage == nil {
		s.storage = &storageQuota{}
	}
	s.storage.extra = extra
	return s
}

type StorageUsage struct {
	UsedBytes  int64 `json:"used_bytes"`
	QuotaBytes int64 `json:"quota_bytes"` // 0 = unlimited
	// -1 = unlimited.
	RemainingBytes int64 `json:"remaining_bytes"`
}

func (s *Store) quotaFor(workspaceID string) int64 {
	if s.storage == nil || s.storage.quota == nil {
		return 0
	}
	return max(s.storage.quota(workspaceID), 0)
}

func (s *Store) workspaceAttachmentBytes(ctx context.Context, workspaceID string) (int64, error) {
	var used sql.NullInt64
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT SUM(a.size) FROM task_attachments a JOIN tasks t ON t.id = a.task_id
		 WHERE t.workspace_id = ?`), workspaceID).Scan(&used)
	if err != nil || s.storage == nil || s.storage.extra == nil {
		return used.Int64, err
	}
	more, err := s.storage.extra(ctx, workspaceID)
	return used.Int64 + more, err
}

func (s *Store) WorkspaceStorage(ctx context.Context, workspaceID string) (StorageUsage, error) {
	used, err := s.workspaceAttachmentBytes(ctx, workspaceID)
	if err != nil {
		return StorageUsage{}, err
	}
	return usageOf(used, s.quotaFor(workspaceID)), nil
}

func usageOf(used, quota int64) StorageUsage {
	u := StorageUsage{UsedBytes: used, QuotaBytes: quota, RemainingBytes: -1}
	if quota > 0 {
		u.RemainingBytes = max(quota-used, 0)
	}
	return u
}

const storageQuotaExceededKey = "task.errors.storage_quota_exceeded"

func IsStorageQuotaExceeded(err error) bool {
	return apperr.Code(err) == storageQuotaExceededKey
}

func storageQuotaExceeded(u StorageUsage) error {
	return apperr.TooLarge(storageQuotaExceededKey,
		"quota", binaryBytes(u.QuotaBytes), "remaining", binaryBytes(u.RemainingBytes))
}

// Writes for a workspace are serialized from Reserve to Done, so parallel uploads can't share the
// same remaining space.
// Always call Done, or the workspace's uploads block.
type AttachmentWriter struct {
	usage StorageUsage
	held  *sync.Mutex
}

// A task not found is treated as unlimited; callers must check the task exists first.
func (s *Store) ReserveAttachment(ctx context.Context, taskID string) (*AttachmentWriter, error) {
	var workspaceID string
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT workspace_id FROM tasks WHERE id = ?`), taskID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return &AttachmentWriter{usage: usageOf(0, 0)}, nil
	}
	if err != nil {
		return nil, err
	}
	return s.ReserveWorkspaceStorage(ctx, workspaceID)
}

func (s *Store) ReserveWorkspaceStorage(ctx context.Context, workspaceID string) (*AttachmentWriter, error) {
	quota := s.quotaFor(workspaceID)
	if quota == 0 {
		return &AttachmentWriter{usage: usageOf(0, 0)}, nil
	}
	held := s.storage.lock(workspaceID)
	held.Lock()
	used, err := s.workspaceAttachmentBytes(ctx, workspaceID)
	if err != nil {
		held.Unlock()
		return nil, err
	}
	w := &AttachmentWriter{usage: usageOf(used, quota), held: held}
	if w.usage.RemainingBytes == 0 {
		w.Done()
		return nil, storageQuotaExceeded(w.usage)
	}
	return w, nil
}

// One byte past the room left lets Check detect overflow without writing the whole overflow.
func (w *AttachmentWriter) Limit(src io.Reader) io.Reader {
	if w.usage.RemainingBytes < 0 {
		return src
	}
	return io.LimitReader(src, w.usage.RemainingBytes+1)
}

func (w *AttachmentWriter) Check(written int64) error {
	if w.usage.RemainingBytes >= 0 && written > w.usage.RemainingBytes {
		return storageQuotaExceeded(w.usage)
	}
	return nil
}

func (w *AttachmentWriter) Done() {
	if w.held != nil {
		w.held.Unlock()
		w.held = nil
	}
}

func binaryBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
