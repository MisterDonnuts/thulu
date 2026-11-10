package task

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/patch"
)

type FlagKind string

const (
	FlagArchDecision FlagKind = "arch_decision"
	FlagInfraImpact  FlagKind = "infra_impact"
	FlagUXDesign     FlagKind = "ux_design"
	FlagCustom       FlagKind = "custom"
)

type FlagStatus string

const (
	FlagPending  FlagStatus = "pending"
	FlagApproved FlagStatus = "approved"
	FlagRejected FlagStatus = "rejected"
)

type Flag struct {
	ID           string          `json:"id"`
	TaskID       string          `json:"task_id"`
	Kind         FlagKind        `json:"kind"`
	Label        string          `json:"label"`
	Status       FlagStatus      `json:"status"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	CreatedBy    string          `json:"created_by"`
	ResolvedBy   string          `json:"resolved_by,omitempty"`
	ResolvedNote string          `json:"resolved_note,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	ResolvedAt   *time.Time      `json:"resolved_at,omitempty"`
}

// The flags API is a compatibility alias over task_approvals for two minors (see approvals.go).

func flagFromApproval(a *Approval) Flag {
	f := Flag{
		ID: a.ID, TaskID: a.TaskID, Kind: FlagKind(a.Key), Label: a.Label, Payload: a.Payload,
		CreatedBy: a.CreatedBy, ResolvedBy: a.DecidedBy, ResolvedNote: a.DecidedNote,
		CreatedAt: a.CreatedAt, ResolvedAt: a.DecidedAt,
	}
	if f.Kind == "" {
		f.Kind = FlagCustom
	}
	switch a.Status {
	case ApprovalApproved, ApprovalWaived:
		f.Status = FlagApproved
	case ApprovalChangesRequested:
		f.Status = FlagRejected
	default:
		f.Status = FlagPending
	}
	return f
}

func FlagsOf(list []Approval) []Flag {
	out := make([]Flag, len(list))
	for i := range list {
		out[i] = flagFromApproval(&list[i])
	}
	return out
}

func ApprovalStatusOf(st FlagStatus) ApprovalStatus {
	switch st {
	case FlagApproved:
		return ApprovalApproved
	case FlagRejected:
		return ApprovalChangesRequested
	default:
		return ApprovalPending
	}
}

func flagSource(createdBy string) ApprovalSource {
	if strings.HasPrefix(createdBy, "agent:") {
		return ApprovalFromAgent
	}
	return ApprovalFromPerson
}

func (s *Store) AddFlag(ctx context.Context, taskID string, f *Flag) error {
	return s.AddFlagFrom(ctx, taskID, f, flagSource(f.CreatedBy))
}

func (s *Store) AddFlagFrom(ctx context.Context, taskID string, f *Flag, source ApprovalSource) error {
	a := Approval{
		ID: f.ID, TaskID: taskID, Key: string(f.Kind), Label: f.Label, Source: source,
		Status: ApprovalStatusOf(f.Status), Payload: f.Payload, CreatedBy: f.CreatedBy, CreatedAt: f.CreatedAt,
	}
	if err := s.insertApproval(ctx, &a); err != nil {
		return err
	}
	*f = flagFromApproval(&a)
	return nil
}

func (s *Store) ListFlags(ctx context.Context, taskID string) ([]Flag, error) {
	list, err := s.ListApprovals(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return FlagsOf(list), nil
}

func (s *Store) GetFlag(ctx context.Context, id string) (*Flag, error) {
	a, err := s.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	f := flagFromApproval(a)
	return &f, nil
}

func (s *Store) PendingFlags(ctx context.Context, taskID string) ([]Flag, error) {
	all, err := s.ListFlags(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := []Flag{}
	for i := range all {
		if all[i].Status == FlagApproved {
			continue
		}
		out = append(out, all[i])
	}
	return out, nil
}

type FlagPatch struct {
	Status *FlagStatus `json:"status,omitempty"`
	Label  *string     `json:"label,omitempty"`
	// Never decoded from a request: the handler stamps it from the resolving credential.
	ResolvedBy   *string `json:"-"`
	ResolvedNote *string `json:"resolved_note,omitempty"`
}

// Does not run what a decision triggers; callers that can move the task must use
// Dispatcher.DecideApproval.
func (s *Store) UpdateFlag(ctx context.Context, id string, p FlagPatch) (*Flag, error) {
	a, err := s.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Label != nil {
		if err := s.SetApprovalLabel(ctx, id, *p.Label); err != nil {
			return nil, err
		}
	}
	if p.Status != nil || p.ResolvedBy != nil || p.ResolvedNote != nil {
		status := a.Status
		if p.Status != nil {
			status = ApprovalStatusOf(*p.Status)
		}
		by, note := a.DecidedBy, a.DecidedNote
		patch.Set(&by, p.ResolvedBy)
		patch.Set(&note, p.ResolvedNote)
		if _, err := s.SetApprovalStatus(ctx, id, status, by, note, a.ApprovedAtRef); err != nil {
			return nil, err
		}
	}
	return s.GetFlag(ctx, id)
}

func (s *Store) DeleteFlag(ctx context.Context, id string) error {
	return s.DeleteApproval(ctx, id)
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}
