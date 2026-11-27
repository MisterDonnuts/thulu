package task

import (
	"context"
	"log"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// The flow flag the refiner sets when a change touches screens; Agentic Testing waits on it.
const SurfaceUI = "ui"

// How many UI paths the note and the event name; the rest is a count.
const surfaceExamples = 5

// The detector is generous on purpose. A false positive costs one Agentic Testing run that finds
// nothing to look at; a false negative ships a screen nobody opened.
var uiExtensions = map[string]bool{
	".tsx": true, ".jsx": true, ".vue": true, ".svelte": true, ".astro": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".sass": true, ".less": true, ".styl": true,
	".erb": true, ".haml": true, ".slim": true, ".hbs": true, ".handlebars": true, ".mustache": true,
	".ejs": true, ".pug": true, ".twig": true, ".njk": true, ".liquid": true, ".jinja": true, ".j2": true,
	".cshtml": true, ".razor": true, ".xib": true, ".storyboard": true, ".svg": true,
}

// Directory names whose files end up on a screen whatever their extension: a .ts under components,
// a .json under locales.
var uiDirs = map[string]bool{
	"components": true, "pages": true, "views": true, "screens": true, "templates": true,
	"layouts": true, "styles": true, "public": true, "static": true, "assets": true, "widgets": true,
	"locales": true, "i18n": true, "translations": true,
}

// Android resource folders under res/ (layout, layout-land, drawable-hdpi, values-es…).
var androidUIResources = []string{"layout", "drawable", "values", "mipmap", "menu", "navigation"}

func isUIPath(p string) bool {
	p = strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/")
	if IsTestPath(p) {
		return false
	}
	if uiExtensions[strings.ToLower(path.Ext(p))] {
		return true
	}
	dirs := strings.Split(path.Dir(p), "/")
	for i, d := range dirs {
		d = strings.ToLower(d)
		if uiDirs[d] {
			return true
		}
		if i > 0 && strings.EqualFold(dirs[i-1], "res") && slices.ContainsFunc(androidUIResources, func(r string) bool {
			return d == r || strings.HasPrefix(d, r+"-")
		}) {
			return true
		}
	}
	return false
}

// UIPaths keeps the paths that look like UI, in the order given.
func UIPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if isUIPath(p) {
			out = append(out, p)
		}
	}
	return out
}

// advanceTarget is nextStageFor plus a look at the code. The refiner names the surfaces before any
// code exists, and the developer may touch a screen the spec never mentioned. So before auto-advance
// passes over a stage that waits on "ui", the daemon reads what the branch changed against the
// default branch: a UI file adds "ui" to the task's flow flags and the stage runs, and so does a
// skip the task set on that stage, which the daemon removes. A diff the daemon cannot read runs the
// stage too; only a task with no project has nothing to read and skips.
func (d *Dispatcher) advanceTarget(ctx context.Context, t *Task, from Stage) (Stage, []Stage) {
	next, skipped := d.nextStageFor(t, from)
	waiting := d.uiStagesIn(t, skipped)
	if len(waiting) == 0 {
		return next, skipped
	}
	hits, unreadable := d.uiChanges(ctx, t)
	switch {
	case len(hits) > 0:
		d.addUIFlagFromDiff(ctx, t, hits, waiting)
		d.unskipForUI(ctx, t, waiting, SkipCauseUIDiff, i18n.Text("task.skips.reasons.ui_diff", "files", uiFilesText(hits)))
	case unreadable != "":
		d.systemComment(ctx, t.ID, NoteOf("task.comments.flow_flags_diff_unreadable",
			"stage", d.stageNote(t, waiting[0]), "err", unreadable))
		// In memory only: nothing confirmed the change touches UI, but nothing ruled it out. nil
		// flow flags already run every surface.
		if t.FlowFlags != nil && !slices.Contains(t.FlowFlags, SurfaceUI) {
			t.FlowFlags = append(slices.Clone(t.FlowFlags), SurfaceUI)
		}
		d.unskipForUI(ctx, t, waiting, SkipCauseDiffUnreadable, i18n.Text("task.skips.reasons.diff_unreadable", "err", unreadable))
	default:
		return next, skipped
	}
	return d.nextStageFor(t, from)
}

// unskipForUI removes the task's skips of the waiting stages: the change may touch a screen, and
// those stages are the ones that look at screens.
func (d *Dispatcher) unskipForUI(ctx context.Context, t *Task, waiting []Stage, cause, reason string) {
	reason = clipReason(reason)
	for _, id := range waiting {
		if _, ok := t.StageSkip(id); !ok {
			continue
		}
		_, _, after, err := d.store.RemoveStageSkip(ctx, t.ID, id, "octopus", reason, cause, false)
		if err != nil {
			logx.Warn(err, "task.log.remove_stage_skip", "task", t.ID, "stage", id)
			continue
		}
		t.StageSkips = after.StageSkips
		d.systemComment(ctx, t.ID, NoteOf("task.comments.skip_removed_system",
			"stage", d.stageNote(t, id), "reason", quoteNote(reason)))
	}
}

// The skipped stages that would run for "ui"; the notes name the first, the one board flows have.
// Empty when nothing waiting on "ui" was skipped: the task has "ui" and skips none of them, or never
// set flow flags and skips none of them.
func (d *Dispatcher) uiStagesIn(t *Task, skipped []Stage) []Stage {
	if t == nil || len(skipped) == 0 {
		return nil
	}
	stages := d.stagesFor(t)
	var out []Stage
	for _, id := range skipped {
		if info, _ := stages.Get(id); slices.Contains(info.SkipUnless, SurfaceUI) {
			out = append(out, id)
		}
	}
	return out
}

// The UI files the task's branches change, project by project, and the first error that kept a
// project's diff from being read ("" when every diff was read).
func (d *Dispatcher) uiChanges(ctx context.Context, t *Task) (hits []string, unreadable string) {
	if d.projects == nil || len(t.ProjectIDs) == 0 {
		return nil, ""
	}
	owner := t.SandboxRootID()
	fail := func(pid string, err error) {
		log.Printf("[task] %s", i18n.Log("task.log.flow_flags_diff_failed", "task", t.ID, "project", pid, "err", err))
		if unreadable == "" {
			unreadable = err.Error()
		}
	}
	for _, pid := range t.ProjectIDs {
		wg, err := d.projects.HostWorktreeGit(ctx, pid, owner)
		if err != nil {
			fail(pid, err)
			continue
		}
		head, err := wg.Exec.Output(wg.Dir, "rev-parse", "HEAD")
		if err != nil {
			fail(pid, err)
			continue
		}
		changed, err := d.projects.WorktreeChangedFiles(ctx, pid, owner, "", strings.TrimSpace(head))
		if err != nil {
			fail(pid, err)
			continue
		}
		hits = append(hits, UIPaths(changed)...)
	}
	return hits, unreadable
}

// Adds "ui" to flow flags that lack it. Flags nobody set already run every surface, and a task that
// has "ui" got here because it skips the stage itself (unskipForUI).
func (d *Dispatcher) addUIFlagFromDiff(ctx context.Context, t *Task, hits []string, waiting []Stage) {
	if t.FlowFlags == nil || slices.Contains(t.FlowFlags, SurfaceUI) {
		return
	}
	flags := append(slices.Clone(t.FlowFlags), SurfaceUI)
	// Set in memory whatever the write does: the stage runs either way.
	t.FlowFlags = flags
	if _, err := d.store.Update(ctx, t.ID, Patch{FlowFlags: &flags}); err != nil {
		logx.Warn(err, "task.log.set_flow_flags", "task", t.ID)
	}
	examples := hits[:min(len(hits), surfaceExamples)]
	files := uiFilesText(hits)
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventFlowFlagsSet, "", "octopus",
		i18n.Text("task.labels.flow_flags_from_diff", "flags", flowFlagsText(flags)),
		map[string]any{"flow_flags": flags, "from_diff": examples}), "task.log.set_flow_flags", "task", t.ID)
	d.systemComment(ctx, t.ID, NoteOf("task.comments.flow_flags_from_diff",
		"files", files, "stage", d.stageNote(t, waiting[0])))
}

// The first UI paths in backticks, then how many more.
func uiFilesText(hits []string) string {
	examples := hits[:min(len(hits), surfaceExamples)]
	files := "`" + strings.Join(examples, "`, `") + "`"
	if rest := len(hits) - len(examples); rest > 0 {
		files += " (+" + strconv.Itoa(rest) + ")"
	}
	return files
}
