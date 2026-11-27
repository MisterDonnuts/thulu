package task

import (
	"context"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// The surfaces the default flow's refiner marks. A board may wait on other values in skip_unless,
// so any value shaped like a stage id is kept; these are only what the skill documents.
var KnownSurfaces = []string{"ui", "perf", "migration", "auth", "api"}

const MaxFlowFlags = 8

var (
	flowRisks = map[string]bool{"low": true, "medium": true, "high": true}
	flowSizes = map[string]bool{"S": true, "M": true, "L": true}
)

// What flags.yaml says about the change itself, next to the flags it raises. Surface nil means the
// file has no surface key: the task's flow flags stay as they are. Skip nil leaves the task's skips
// as they are too.
type flowSpec struct {
	Surface *[]string   `yaml:"surface"`
	Risk    string      `yaml:"risk"`
	Size    string      `yaml:"size"`
	Skip    *[]skipYAML `yaml:"skip"`
}

// Lowercased and deduplicated, in the order given. Any value a skip_unless could not hold makes the
// whole list invalid: dropping it could skip a stage the spec meant to run.
func NormalizeFlowFlags(in []string) (out, invalid []string) {
	out = []string{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if !types.ValidStageID(v) {
			invalid = append(invalid, v)
			continue
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) > MaxFlowFlags {
		invalid = append(invalid, out[MaxFlowFlags:]...)
	}
	return out, invalid
}

func (f flowSpec) risk() string {
	if r := strings.ToLower(strings.TrimSpace(f.Risk)); flowRisks[r] {
		return r
	}
	return ""
}

func (f flowSpec) size() string {
	if s := strings.ToUpper(strings.TrimSpace(f.Size)); flowSizes[s] {
		return s
	}
	return ""
}

// What ParseAndAddFlags did with one flags.yaml.
type FlagsImport struct {
	Created int `json:"flags_created"`
	// Set only when the file carried a valid surface list, which then replaced the task's flow
	// flags.
	FlowFlags []string `json:"flow_flags,omitempty"`
	Risk      string   `json:"risk,omitempty"`
	Size      string   `json:"size,omitempty"`
	// Surface values refused; the task's flow flags were left as they were.
	Invalid []string `json:"invalid_surface,omitempty"`
	// The task's skips after the file's `skip:` list; empty when the file has none.
	StageSkips []StageSkip `json:"stage_skips,omitempty"`
	// The `skip:` entries refused, each with why; their columns run.
	SkipsRefused []SkipRefusal `json:"skips_refused,omitempty"`
}

// Writes surface, risk, size and skips from flags.yaml. Only the refinement stage's file
// (ingest_flags) and a person reach here; tags would not do, since any run key edits them.
func (s *Store) applyFlowSpec(ctx context.Context, taskID string, actor SkipActor, spec flowSpec, out *FlagsImport) {
	s.applyFlowSurface(ctx, taskID, actor.By, spec, out)
	s.applyFlowSkips(ctx, taskID, actor, spec.Skip, out)
}

func (s *Store) applyFlowSurface(ctx context.Context, taskID, createdBy string, spec flowSpec, out *FlagsImport) {
	out.Risk, out.Size = spec.risk(), spec.size()
	if spec.Surface == nil {
		if out.Risk != "" || out.Size != "" {
			s.recordFlowSpec(ctx, taskID, createdBy, nil, out)
		}
		return
	}
	flags, invalid := NormalizeFlowFlags(*spec.Surface)
	if len(invalid) > 0 {
		out.Invalid = invalid
		_, err := s.AddNote(ctx, taskID, "", SourceSystem, "octopus", NoteOf("task.comments.flow_flags_invalid",
			"values", strings.Join(invalid, ", "), "max", MaxFlowFlags))
		logx.Warn(err, "task.log.flow_flags_invalid", "task", taskID)
		return
	}
	if _, err := s.Update(ctx, taskID, Patch{FlowFlags: &flags}); err != nil {
		logx.Warn(err, "task.log.set_flow_flags", "task", taskID)
		return
	}
	out.FlowFlags = flags
	s.recordFlowSpec(ctx, taskID, createdBy, flags, out)
}

func (s *Store) recordFlowSpec(ctx context.Context, taskID, createdBy string, flags []string, out *FlagsImport) {
	payload := map[string]any{"risk": out.Risk, "size": out.Size}
	if flags != nil {
		payload["flow_flags"] = flags
	}
	logx.Warn(s.RecordEvent(ctx, taskID, EventFlowFlagsSet, "", createdBy,
		i18n.Text("task.labels.flow_flags_summary", "flags", flowFlagsText(flags), "risk", dash(out.Risk), "size", dash(out.Size)),
		payload), "task.log.set_flow_flags", "task", taskID)
}

func flowFlagsText(flags []string) string {
	if len(flags) == 0 {
		return "—" //i18n:ignore placeholder for an empty list
	}
	return strings.Join(flags, ", ")
}

func dash(s string) string {
	if s == "" {
		return "—" //i18n:ignore placeholder for a missing value
	}
	return s
}

// Whether a stage with skip_unless runs for this task. nil flow flags (nobody refined it) run
// everything, so a task that skipped refinement never loses its QA.
func stageRunsFor(info types.StageInfo, flowFlags []string) bool {
	if len(info.SkipUnless) == 0 || flowFlags == nil {
		return true
	}
	for _, want := range info.SkipUnless {
		if slices.Contains(flowFlags, want) {
			return true
		}
	}
	return false
}
