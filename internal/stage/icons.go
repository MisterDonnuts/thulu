package stage

// Every slug must have a matching <symbol id="i-tt-…"> in web/src/components/IconSprite.astro
// (enforced by icons_test.go).
var TaskTypeIcons = []string{
	"bug",
	"feature",
	"chore",
	"spike",
	"rocket",
	"shield",
	"book",
	"bolt",
	"refresh",
	"question",
	"alert",
	"palette",
	"plug",
	"database",
	"checklist",
	"clock",
	"star",
	"users",
}

var taskTypeIconSet = func() map[string]bool {
	m := make(map[string]bool, len(TaskTypeIcons))
	for _, i := range TaskTypeIcons {
		m[i] = true
	}
	return m
}()

func ValidTaskTypeIcon(icon string) bool { return taskTypeIconSet[icon] }
