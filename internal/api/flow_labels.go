package api

import (
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/workshop"
)

// The catalog flow a board was installed from, when this build still ships it. Its names were
// written in the installer's locale; these helpers render them in the viewer's.
func boardFlow(b stagepkg.BoardInfo) (*workshop.FlowItem, bool) {
	if b.Flow == nil {
		return nil, false
	}
	cat, err := workshopCatalog()
	if err != nil {
		return nil, false
	}
	f, ok := cat.Flow(b.Flow.Name)
	if !ok {
		return nil, false
	}
	return &f, true
}

func localizeBoard(b stagepkg.BoardInfo, locale string) stagepkg.BoardInfo {
	b = stagepkg.LocalizeSeededBoard(b, locale)
	if f, ok := boardFlow(b); ok {
		b = f.LocalizeBoard(b, locale)
	}
	return b
}

func localizeColumns(b stagepkg.BoardInfo, cols []types.StageInfo, locale string) []types.StageInfo {
	cols = stagepkg.LocalizeSeeded(cols, locale)
	if f, ok := boardFlow(b); ok {
		cols = f.LocalizeStages(cols, locale)
	}
	return cols
}

func localizeTypes(b stagepkg.BoardInfo, tt []stagepkg.TaskTypeInfo, locale string) {
	if f, ok := boardFlow(b); ok {
		f.LocalizeTypes(tt, locale)
	}
}
