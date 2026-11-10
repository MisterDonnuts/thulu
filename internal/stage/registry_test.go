package stage

import (
	"reflect"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestStaticIsTheBareBoard(t *testing.T) {
	want := []struct {
		id    types.Stage
		label string
	}{
		{types.StageBacklog, "Backlog"},
		{types.StageInProgress, "In progress"},
		{types.StageDone, "Done"},
	}
	got := Static().List()
	if len(got) != len(want) {
		t.Fatalf("List() len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.ID != w.id || g.Label != w.label {
			t.Errorf("stage[%d] = %s/%s, want %s/%s", i, g.ID, g.Label, w.id, w.label)
		}
		if g.AutoAdvance || g.BlockedByFlags || g.IngestFlags || g.OpenPR {
			t.Errorf("%s carries agent-flow flags: %+v", g.ID, g)
		}
		if g.Virtual {
			t.Errorf("%s: board stage must not be virtual", g.ID)
		}
	}
	reg := Static()
	if got := reg.Initial(); got != types.StageBacklog {
		t.Errorf("Initial() = %s, want backlog", got)
	}
	if got := reg.Terminals(); len(got) != 1 || got[0] != types.StageDone {
		t.Errorf("Terminals() = %v, want [done]", got)
	}
	if got := reg.KickbackDefault(); got != types.StageInProgress {
		t.Errorf("KickbackDefault() = %s, want in_progress", got)
	}
	if in, _ := reg.Get(types.StageInProgress); !in.PushBranch || in.WipGroup != types.WipGroupWork {
		t.Errorf("in_progress = push:%v wip:%q, want push:true wip:%q", in.PushBranch, in.WipGroup, types.WipGroupWork)
	}
}

func TestFlowSemanticFlags(t *testing.T) {
	reg := NewSnapshot(stagetest.Flow())
	single := []struct {
		name string
		want types.Stage
		get  func(types.StageInfo) bool
	}{
		{"initial", types.StageBacklog, func(i types.StageInfo) bool { return i.Initial }},
		{"terminal", types.StageDone, func(i types.StageInfo) bool { return i.Terminal }},
		{"ingest_flags", types.StageRefinement, func(i types.StageInfo) bool { return i.IngestFlags }},
		{"push_branch", types.StageInProgress, func(i types.StageInfo) bool { return i.PushBranch }},
		{"open_pr", types.StageHandoff, func(i types.StageInfo) bool { return i.OpenPR }},
		{"blocked_by_flags", types.StageReview, func(i types.StageInfo) bool { return i.BlockedByFlags }},
	}
	for _, s := range single {
		var holders []types.Stage
		for _, info := range reg.List() {
			if s.get(info) {
				holders = append(holders, info.ID)
			}
		}
		if len(holders) != 1 || holders[0] != s.want {
			t.Errorf("%s holders = %v, want exactly [%s]", s.name, holders, s.want)
		}
	}
	if got := reg.KickbackDefault(); got != types.StageRefinement {
		t.Errorf("KickbackDefault() = %s, want refinement", got)
	}
}

func TestNextWalksTheFullBoard(t *testing.T) {
	reg := Static()
	board := reg.List()
	cur := reg.Initial()
	visited := []types.Stage{cur}
	for {
		next := reg.Next(cur)
		if next == "" {
			break
		}
		visited = append(visited, next)
		cur = next
		if len(visited) > len(board) {
			t.Fatalf("Next() cycled: %v", visited)
		}
	}
	if len(visited) != len(board) {
		t.Fatalf("walk visited %d stages, want %d (%v)", len(visited), len(board), visited)
	}
	if cur != types.StageDone {
		t.Errorf("walk ended at %s, want done", cur)
	}
	if reg.Next(types.StageDone) != "" {
		t.Error("terminal stage must have no Next")
	}
	if reg.Next("bogus") != "" {
		t.Error("unknown stage must have no Next")
	}
}

func TestVirtualStages(t *testing.T) {
	reg := Static()
	for _, v := range types.VirtualStages {
		if !reg.Valid(v) {
			t.Errorf("virtual %s must be Valid (thulu.stages declares it)", v)
		}
		if reg.Next(v) != "" {
			t.Errorf("virtual %s must have no Next", v)
		}
		info, ok := reg.Get(v)
		if !ok || !info.Virtual {
			t.Errorf("Get(%s) = (%+v, %v), want virtual info", v, info, ok)
		}
		if info.AutoAdvance {
			t.Errorf("virtual %s must never auto-advance nor gate transitions", v)
		}
	}
	for _, st := range reg.List() {
		if st.ID == types.StageMention || st.ID == types.StageChat {
			t.Errorf("virtual stage %s must not be a board column", st.ID)
		}
	}
	withVirtual := reg.ListWithVirtual()
	if len(withVirtual) != len(reg.List())+len(types.VirtualStages) {
		t.Errorf("ListWithVirtual len = %d, want board+%d", len(withVirtual), len(types.VirtualStages))
	}
}

func TestGetUnknownEchoesZeroFlags(t *testing.T) {
	info, ok := Static().Get("never_created")
	if ok {
		t.Error("unknown stage must report ok=false")
	}
	if info.ID != "never_created" || info.Label != "never_created" {
		t.Errorf("unknown Get() = %+v, want ID+Label echo", info)
	}
	if info.Initial || info.Terminal || info.AutoAdvance ||
		info.BlockedByFlags || info.IngestFlags || info.PushBranch || info.OpenPR {
		t.Errorf("unknown stage must carry zero flags: %+v", info)
	}
}

func TestNewSnapshotSortsByPosition(t *testing.T) {
	reg := NewSnapshot([]types.StageInfo{
		{ID: "last", Position: 30, Terminal: true},
		{ID: "first", Position: 10, Initial: true},
		{ID: "mid", Position: 20},
	})
	got := reg.List()
	wantOrder := []types.Stage{"first", "mid", "last"}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Fatalf("List()[%d] = %s, want %s (full: %v)", i, got[i].ID, w, got)
		}
	}
	if reg.Next("first") != "mid" || reg.Next("mid") != "last" || reg.Next("last") != "" {
		t.Error("Next() must follow position order and stop at terminal")
	}
	if reg.KickbackDefault() != "mid" {
		t.Errorf("KickbackDefault() = %s, want mid (first non-initial non-terminal)", reg.KickbackDefault())
	}
}

func TestNewSnapshotDefensiveFallbacks(t *testing.T) {
	reg := NewSnapshot([]types.StageInfo{
		{ID: "a", Position: 1},
		{ID: "b", Position: 2, Terminal: true},
	})
	if reg.Initial() != "a" {
		t.Errorf("Initial() fallback = %s, want first stage", reg.Initial())
	}
	if reg.KickbackDefault() != "a" {
		t.Errorf("KickbackDefault() fallback = %s, want initial", reg.KickbackDefault())
	}

	empty := NewSnapshot(nil)
	if empty.Initial() != "" || len(empty.Terminals()) != 0 {
		t.Error("empty board answers empty, never panics")
	}
	if _, ok := empty.Get(types.StageChat); !ok {
		t.Error("virtuals exist even on an empty board")
	}
}

func TestSnapshotImmutability(t *testing.T) {
	input := []types.StageInfo{
		{ID: "a", Position: 1, Initial: true},
		{ID: "b", Position: 2, Terminal: true},
	}
	reg := NewSnapshot(input)
	input[0].ID = "mutated"
	if _, ok := reg.Get("a"); !ok {
		t.Error("registry must copy its input")
	}
	out := reg.List()
	out[0].ID = "mutated_out"
	if got := reg.List()[0].ID; got != "a" {
		t.Errorf("List() must return a copy, got %s", got)
	}
	terms := reg.Terminals()
	if len(terms) > 0 {
		terms[0] = "mutated_terms"
		if reg.Terminals()[0] != "b" {
			t.Error("Terminals() must return a copy")
		}
	}
}

func TestSeedFromKeepsCatalogIdentity(t *testing.T) {
	got := SeedFrom([]types.StageInfo{
		{ID: types.StageInProgress, Label: "Desarrollo", Color: "#123abc", AutoAdvance: true},
		{ID: "unrelated", Label: "Unrelated"},
	}, i18n.DaemonLocale())
	want := Defaults()
	want[1].Label, want[1].Color = "Desarrollo", "#123abc"
	if len(got) != len(want) {
		t.Fatalf("SeedFrom = %d stages, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("stage[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !reflect.DeepEqual(SeedFrom(nil, i18n.DaemonLocale()), Defaults()) {
		t.Error("SeedFrom(nil) must be Defaults()")
	}
}

func TestDefaultsInNamesColumnsInTheLocale(t *testing.T) {
	es := DefaultsIn("es")
	en := DefaultsIn("en")
	if len(es) != len(en) {
		t.Fatalf("DefaultsIn: %d columns in es, %d in en", len(es), len(en))
	}
	if es[1].Label == en[1].Label {
		t.Errorf("in_progress has the same name in es and en: %q", es[1].Label)
	}
	if es[1].Label != i18n.T("es", "stage.labels.in_progress") {
		t.Errorf("es in_progress = %q", es[1].Label)
	}
	for i := range es {
		es[i].Label, en[i].Label = "", ""
	}
	if !reflect.DeepEqual(es, en) {
		t.Error("DefaultsIn changed more than the labels between locales")
	}
	if !reflect.DeepEqual(SeedFrom(nil, "es"), DefaultsIn("es")) {
		t.Error("SeedFrom(nil, es) must be DefaultsIn(es)")
	}
}

func TestSeededName(t *testing.T) {
	if got, ok := SeededName("es", "review"); !ok || got != i18n.T("es", "stage.labels.review") {
		t.Errorf("SeededName(review) = %q, %v; want the es name", got, ok)
	}
	if got, ok := SeededName("es", "qa_custom"); ok || got != "" {
		t.Errorf("SeededName(qa_custom) = %q, %v; want none", got, ok)
	}
}

func TestLocalizeSeeded(t *testing.T) {
	in := []types.StageInfo{
		{ID: types.StageBacklog, Label: i18n.T("en", "stage.labels.backlog")},
		{ID: types.StageInProgress, Label: i18n.T("en", "stage.labels.in_progress")},
		{ID: types.StageDone, Label: "Shipped"},
		{ID: "custom", Label: "In progress"},
	}
	got := LocalizeSeeded(in, "es")
	if got[1].Label != i18n.T("es", "stage.labels.in_progress") {
		t.Errorf("seeded in_progress = %q, want the es name", got[1].Label)
	}
	if got[2].Label != "Shipped" {
		t.Errorf("renamed done = %q, want it untouched", got[2].Label)
	}
	if got[3].Label != "In progress" {
		t.Errorf("a custom id that happens to read like a seed = %q, want it untouched", got[3].Label)
	}
	if in[1].Label != i18n.T("en", "stage.labels.in_progress") {
		t.Error("LocalizeSeeded must not write through to its input")
	}

	b := LocalizeSeededBoard(BoardInfo{
		Label:       i18n.T("en", "stage.labels.default_board"),
		Description: i18n.T("en", "stage.descriptions.default_board"),
	}, "es")
	if b.Label != i18n.T("es", "stage.labels.default_board") ||
		b.Description != i18n.T("es", "stage.descriptions.default_board") {
		t.Errorf("seeded board = %+v, want the es name and description", b)
	}
	if got := LocalizeSeededBoard(BoardInfo{Label: "Product"}, "es"); got.Label != "Product" {
		t.Errorf("renamed board = %q, want it untouched", got.Label)
	}
}

func TestFactoryColumnsFollowTheReader(t *testing.T) {
	locales := i18n.Default().Locales()
	for _, seed := range locales {
		for _, reader := range locales {
			got := LocalizeSeeded(DefaultsIn(seed), reader)
			want := DefaultsIn(reader)
			for i := range want {
				if got[i].Label != want[i].Label {
					t.Errorf("seeded in %s, read in %s: %s = %q, want %q",
						seed, reader, want[i].ID, got[i].Label, want[i].Label)
				}
			}
		}
	}
}

func TestLocalizeSeededName(t *testing.T) {
	cases := []struct {
		name, locale, want string
		ok                 bool
	}{
		{i18n.T("en", "stage.labels.in_progress"), "es", i18n.T("es", "stage.labels.in_progress"), true},
		{i18n.T("es", "stage.labels.done"), "en", i18n.T("en", "stage.labels.done"), true},
		{"in_progress", "es", i18n.T("es", "stage.labels.in_progress"), true},
		{"Triage", "es", "Triage", false},
		{"", "es", "", false},
	}
	for _, c := range cases {
		got, ok := LocalizeSeededName(c.name, c.locale)
		if got != c.want || ok != c.ok {
			t.Errorf("LocalizeSeededName(%q, %s) = %q, %v; want %q, %v", c.name, c.locale, got, ok, c.want, c.ok)
		}
	}
}
