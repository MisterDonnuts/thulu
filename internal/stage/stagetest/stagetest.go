package stagetest

import (
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func Flow() []types.StageInfo {
	return []types.StageInfo{
		{ID: types.StageBacklog, Label: i18n.Text("stage.labels.backlog"), Color: "#5d6477", Position: 10,
			Initial: true},
		{ID: types.StageRefinement, Label: i18n.Text("stage.labels.refinement"), Color: "#56ccf2", Position: 20,
			AutoAdvance: true, IngestFlags: true},
		{ID: types.StageReview, Label: i18n.Text("stage.labels.review"), Color: "#bb86fc", Position: 30,
			BlockedByFlags: true},
		{ID: types.StageInProgress, Label: i18n.Text("stage.labels.in_progress"), Color: "#f2994a", Position: 40,
			AutoAdvance: true, PushBranch: true, WipGroup: types.WipGroupWork},
		{ID: types.StageAIReview, Label: i18n.Text("stage.labels.ai_review"), Color: "#f2c94c", Position: 50,
			AutoAdvance: true, WipGroup: types.WipGroupReview},
		{ID: types.StageTesting, Label: i18n.Text("stage.labels.testing"), Color: "#27ae60", Position: 60,
			AutoAdvance: true, WipGroup: types.WipGroupReview},
		{ID: types.StageHandoff, Label: i18n.Text("stage.labels.handoff"), Color: "#bb86fc", Position: 70,
			OpenPR: true, WipGroup: types.WipGroupReview},
		{ID: types.StageRelease, Label: i18n.Text("stage.labels.release"), Color: "#6fcf97", Position: 80,
			AutoAdvance: true},
		{ID: types.StageDone, Label: i18n.Text("stage.labels.done"), Color: "#3aa667", Position: 90,
			Terminal: true},
	}
}
