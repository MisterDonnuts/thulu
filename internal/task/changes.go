package task

import "github.com/MisterDonnuts/thulu/internal/syncx"

type ChangeKind string

const (
	ChangeCreated ChangeKind = "task_created"
	ChangeUpdated ChangeKind = "task_updated"
	ChangeDeleted ChangeKind = "task_deleted"
)

// Deliberately thin: consumers refetch over REST so only one code path produces the task shape.
type Change struct {
	Kind        ChangeKind `json:"kind"`
	TaskID      string     `json:"task_id"`
	WorkspaceID string     `json:"workspace_id"`
	BoardID     string     `json:"board_id,omitempty"`
	Stage       string     `json:"stage,omitempty"`
	ParentID    string     `json:"parent_id,omitempty"`
}

type ChangeBus = syncx.Bus[Change]

func NewChangeBus() *ChangeBus { return syncx.NewBus[Change]() }

// Must be set at boot, before the store is used concurrently; there is no lock.
func (s *Store) WithChangeBus(b *ChangeBus) *Store {
	s.changes = b
	return s
}

// Call only after the write commits, or browsers refetch rows a rolled-back transaction never
// changed.
func (s *Store) notify(kind ChangeKind, t *Task) {
	if s.changes == nil || t == nil {
		return
	}
	s.changes.Publish(Change{
		Kind:        kind,
		TaskID:      t.ID,
		WorkspaceID: t.WorkspaceID,
		BoardID:     t.BoardID,
		Stage:       string(t.Stage),
		ParentID:    t.ParentID,
	})
}
