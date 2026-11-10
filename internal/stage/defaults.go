package stage

import (
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func Defaults() []types.StageInfo { return DefaultsIn(i18n.DaemonLocale()) }

func DefaultsIn(locale string) []types.StageInfo {
	return []types.StageInfo{
		{ID: types.StageBacklog, Label: i18n.T(locale, "stage.labels.backlog"), Color: "#5d6477", Position: 10,
			Initial: true},
		{ID: types.StageInProgress, Label: i18n.T(locale, "stage.labels.in_progress"), Color: "#f2994a", Position: 20,
			PushBranch: true, WipGroup: types.WipGroupWork},
		{ID: types.StageDone, Label: i18n.T(locale, "stage.labels.done"), Color: "#3aa667", Position: 30,
			Terminal: true},
	}
}

func seededLabel(label, key string) bool {
	if label == "" {
		return false
	}
	cat := i18n.Default()
	for _, l := range cat.Locales() {
		if cat.Has(l, key) && cat.Render(l, key) == label {
			return true
		}
	}
	return false
}

func SeededLabelKey(stage types.Stage, label string) (string, bool) {
	key := "stage.labels." + string(stage) //i18n:ignore a catalog key prefix, not a message
	return key, seededLabel(label, key)
}

func SeededName(locale string, stage types.Stage) (string, bool) {
	key := "stage.labels." + string(stage) //i18n:ignore a catalog key prefix, not a message
	if !i18n.Default().Has(i18n.SourceLocale, key) {
		return "", false
	}
	return i18n.T(locale, key), true
}

func LocalizeSeededName(name, locale string) (string, bool) {
	if name == "" {
		return "", false
	}
	const prefix = "stage.labels." //i18n:ignore a catalog key prefix, not a message
	cat := i18n.Default()
	if cat.Has(i18n.SourceLocale, prefix+name) {
		return i18n.T(locale, prefix+name), true
	}
	for _, key := range cat.Keys(i18n.SourceLocale) {
		if strings.HasPrefix(key, prefix) && seededLabel(name, key) {
			return i18n.T(locale, key), true
		}
	}
	return name, false
}

func LocalizeSeeded(stages []types.StageInfo, locale string) []types.StageInfo {
	out := make([]types.StageInfo, len(stages))
	copy(out, stages)
	for i := range out {
		key := "stage.labels." + string(out[i].ID) //i18n:ignore a catalog key prefix, not a message
		if seededLabel(out[i].Label, key) {
			out[i].Label = i18n.T(locale, key)
		}
	}
	return out
}

func LocalizeSeededBoard(b BoardInfo, locale string) BoardInfo {
	if seededLabel(b.Label, "stage.labels.default_board") {
		b.Label = i18n.T(locale, "stage.labels.default_board")
	}
	if seededLabel(b.Description, "stage.descriptions.default_board") {
		b.Description = i18n.T(locale, "stage.descriptions.default_board")
	}
	return b
}

func SeedFrom(catalog []types.StageInfo, locale string) []types.StageInfo {
	known := make(map[types.Stage]types.StageInfo, len(catalog))
	for i := range catalog {
		known[catalog[i].ID] = catalog[i]
	}
	out := DefaultsIn(locale)
	for i := range out {
		if ident, ok := known[out[i].ID]; ok {
			out[i].Label, out[i].Color = ident.Label, ident.Color
		}
	}
	return out
}

func Virtuals() []types.StageInfo {
	return []types.StageInfo{
		{ID: types.StageMention, Label: i18n.Text("stage.labels.mention"), Color: "#9aa0b4", Virtual: true},
		{ID: types.StageChat, Label: i18n.Text("stage.labels.chat"), Color: "#9aa0b4", Virtual: true},
	}
}
