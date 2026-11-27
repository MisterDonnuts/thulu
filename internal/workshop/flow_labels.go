package workshop

import (
	"slices"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// An install writes the flow's names in the installer's locale. A stored name that still equals
// what the flow ships in some locale reads in the viewer's; one a person renamed stays as written.

// LocalizeBoard renders the board's label and description in locale when they are the flow's own.
func (f *FlowItem) LocalizeBoard(b stagepkg.BoardInfo, locale string) stagepkg.BoardInfo {
	b.Label = relabel(b.Label, locale, func(l string) string { return pick(f.Labels, l, f.Label) })
	b.Description = relabel(b.Description, locale, func(l string) string {
		return pick(f.Descriptions, l, f.Description)
	})
	return b
}

// LocalizeStages returns a copy of stages with the flow's column and approval names in locale.
func (f *FlowItem) LocalizeStages(stages []types.StageInfo, locale string) []types.StageInfo {
	out := make([]types.StageInfo, len(stages))
	copy(out, stages)
	for i := range out {
		fst := f.stage(out[i].ID)
		if fst == nil {
			continue
		}
		out[i].Label = relabel(out[i].Label, locale, func(l string) string {
			return stageLabel(fst.Labels, l, fst.ID)
		})
		out[i].RequiredApprovals = localizeApprovals(out[i].RequiredApprovals, fst.RequiredApprovals, locale)
	}
	return out
}

// LocalizeTypes renders the flow's task type names in locale, in place.
func (f *FlowItem) LocalizeTypes(tt []stagepkg.TaskTypeInfo, locale string) {
	for i := range tt {
		ft := f.taskType(tt[i].ID)
		if ft == nil {
			continue
		}
		tt[i].Label = relabel(tt[i].Label, locale, func(l string) string {
			return pick(ft.Labels, l, ft.Label)
		})
	}
}

func localizeApprovals(stored []types.ApprovalRequirement, shipped []FlowApproval, locale string) []types.ApprovalRequirement {
	if len(stored) == 0 {
		return stored
	}
	out := make([]types.ApprovalRequirement, len(stored))
	copy(out, stored)
	for i := range out {
		for j := range shipped {
			a := &shipped[j]
			if a.Key != out[i].Key {
				continue
			}
			out[i].Label = relabel(out[i].Label, locale, func(l string) string { return pick(a.Labels, l, a.Label) })
		}
	}
	return out
}

// relabel returns render(locale) when stored equals render(l) for some locale l, else stored.
func relabel(stored, locale string, render func(locale string) string) string {
	if stored == "" {
		return stored
	}
	if slices.ContainsFunc(i18n.Default().Locales(), func(l string) bool { return render(l) == stored }) {
		return render(locale)
	}
	return stored
}

func (f *FlowItem) stage(id types.Stage) *FlowStage {
	for i := range f.Stages {
		if f.Stages[i].ID == id {
			return &f.Stages[i]
		}
	}
	return nil
}

func (f *FlowItem) taskType(id string) *FlowType {
	for i := range f.Types {
		if f.Types[i].ID == id {
			return &f.Types[i]
		}
	}
	return nil
}
