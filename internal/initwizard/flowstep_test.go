package initwizard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func TestInterviewOffersTheThuluFlowByDefault(t *testing.T) {
	cfg, out := transcript(t, "2\n2\n\n3\n\n\n\ns\n\n\n\n\n", nil)
	if cfg.Board != ThuluFlow || !cfg.installsBoard() {
		t.Errorf("board = %q keep=%v: the setup does not install the Thulu flow on the defaults", cfg.Board, cfg.KeepBoard)
	}
	if !strings.Contains(out, i18n.Text("initwizard.prompts.board")) {
		t.Errorf("the setup never asks about the board:\n%s", out)
	}
	if !strings.Contains(out, i18n.Text("initwizard.options.board_minimal.label")) {
		t.Errorf("the setup does not offer the minimal start:\n%s", out)
	}
	if strings.Contains(out, i18n.Text("initwizard.options.board_keep.label")) {
		t.Errorf("a first install offers to leave a board it does not have:\n%s", out)
	}
}

func askBoard(t *testing.T, iv *Interviewer, answers string) string {
	t.Helper()
	var out bytes.Buffer
	iv.P, iv.Ctx = NewTestPrompter(strings.NewReader(answers), &out), context.Background()
	if iv.Cfg == nil {
		iv.Cfg = &Config{}
	}
	if err := iv.askFlow(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestAskFlowTakesEitherBoard(t *testing.T) {
	for answer, want := range map[string]string{"\n": ThuluFlow, "1\n": ThuluFlow, "2\n": MinimalFlow} {
		iv := &Interviewer{}
		askBoard(t, iv, answer)
		if iv.Cfg.Board != want || !iv.Cfg.installsBoard() {
			t.Errorf("answer %q: board = %q keep=%v, want %q installed", answer, iv.Cfg.Board, iv.Cfg.KeepBoard, want)
		}
	}
}

// A first install has no "nothing at all": a third option does not exist, so the answer is asked
// again and input that ends falls back to the default.
func TestAskFlowOffersNoEmptyBoardOnAFirstInstall(t *testing.T) {
	iv := &Interviewer{}
	out := askBoard(t, iv, "3\n")
	if iv.Cfg.Board != ThuluFlow || iv.Cfg.KeepBoard {
		t.Errorf("board = %q keep=%v after an answer out of range", iv.Cfg.Board, iv.Cfg.KeepBoard)
	}
	if !strings.Contains(out, i18n.Text("initwizard.prompts.pick_number", "max", 2)) {
		t.Errorf("the out-of-range answer was not refused:\n%s", out)
	}
}

func TestAskFlowLeavesTheBoardsAloneOnARerun(t *testing.T) {
	for _, previous := range []string{ThuluFlow, MinimalFlow, ""} {
		iv := &Interviewer{Rerun: true, PreviousBoard: previous}
		out := askBoard(t, iv, "\n")
		if !strings.Contains(out, i18n.Text("initwizard.options.board_keep.label")) {
			t.Errorf("previous %q: a rerun does not offer to leave the boards alone:\n%s", previous, out)
		}
		if iv.Cfg.installsBoard() || iv.Cfg.Board != previous {
			t.Errorf("previous %q: Enter gives board=%q keep=%v, want the previous board kept, nothing installed",
				previous, iv.Cfg.Board, iv.Cfg.KeepBoard)
		}
	}

	iv := &Interviewer{Rerun: true, PreviousBoard: ThuluFlow}
	askBoard(t, iv, "2\n")
	if iv.Cfg.Board != MinimalFlow || !iv.Cfg.installsBoard() {
		t.Errorf("a rerun that picks the minimal start: board=%q keep=%v", iv.Cfg.Board, iv.Cfg.KeepBoard)
	}
}

func TestLoadPreviousTellsAFirstInstallFromARerun(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	w := &run{opts: Options{Root: root}}
	iv := &Interviewer{}
	w.loadPrevious(iv)
	if iv.Rerun {
		t.Error("an install without an answers file reads as a rerun, so the setup would not suggest a flow")
	}

	if err := SaveAnswers(root, &Config{Mode: ModeDocker, Board: MinimalFlow}); err != nil {
		t.Fatal(err)
	}
	iv = &Interviewer{}
	w.loadPrevious(iv)
	if !iv.Rerun || iv.PreviousBoard != MinimalFlow {
		t.Errorf("rerun=%v previous board=%q, want a rerun over the minimal start", iv.Rerun, iv.PreviousBoard)
	}
}

// Answers files written before the board choice say install_flow true or false.
func TestLoadAnswersReadsTheOldFlowAnswer(t *testing.T) {
	for _, c := range []struct {
		body, want string
	}{
		{`{"mode":"docker","install_flow":true}`, ThuluFlow},
		{`{"mode":"docker","install_flow":false}`, ""},
		{`{"mode":"docker"}`, ""},
		{`{"mode":"docker","board":"thulu-minimal"}`, MinimalFlow},
	} {
		root := t.TempDir()
		path := filepath.Join(root, AnswersFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadAnswers(root)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Board != c.want {
			t.Errorf("%s: board = %q, want %q", c.body, cfg.Board, c.want)
		}
		if err := SaveAnswers(root, cfg); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "install_flow") {
			t.Errorf("%s: saving again writes the old field:\n%s", c.body, body)
		}
	}
}

// fakeWorkshop answers /workshop/plan with resolution for the flow it is asked about and records
// each apply body.
type fakeWorkshop struct {
	resolution string
	blocked    bool
	applyFails bool
	applied    []map[string]any
}

var flowBoards = map[string]string{ThuluFlow: "thulu", MinimalFlow: "thulu_minimal"}

func (f *fakeWorkshop) serve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items       []flowRef `json:"items"`
		FlowDefault bool      `json:"flow_default"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := ""
	if len(body.Items) == 1 {
		name = body.Items[0].Name
	}
	plan := map[string]any{
		"blocked": f.blocked,
		"items": []map[string]any{
			{"kind": "agent", "name": "thulu-agent", "resolution": "install", "target": "thulu-agent"},
			{"kind": "flow", "name": name, "resolution": f.resolution, "target": flowBoards[name]},
		},
	}
	switch r.URL.Path {
	case "/workshop/plan":
		_ = json.NewEncoder(w).Encode(plan)
	case "/workshop/apply":
		f.applied = append(f.applied, map[string]any{"name": name, "flow_default": body.FlowDefault})
		if f.applyFails {
			http.Error(w, `{"error":"stage store unavailable"}`, http.StatusInternalServerError)
			return
		}
		plan["items"] = []map[string]any{{"kind": "flow", "name": name, "resolution": "install", "target": flowBoards[name] + "_2"}}
		_ = json.NewEncoder(w).Encode(map[string]any{"plan": plan, "result": map[string]any{}})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeWorkshop) start(t *testing.T) *APIClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return NewAPIClient(srv.URL, "k")
}

func TestInstallFlowCreatesTheDefaultBoard(t *testing.T) {
	for _, flow := range []string{ThuluFlow, MinimalFlow} {
		ws := &fakeWorkshop{resolution: "install"}
		board, installed, err := ws.start(t).InstallFlow(context.Background(), flow)
		if err != nil || !installed {
			t.Fatalf("%s: installed=%v err=%v", flow, installed, err)
		}
		if board != flowBoards[flow]+"_2" {
			t.Errorf("%s: board = %q, want the id the apply used", flow, board)
		}
		if len(ws.applied) != 1 || ws.applied[0]["name"] != flow || ws.applied[0]["flow_default"] != true {
			t.Fatalf("%s: apply bodies = %v, want one for the flow with flow_default", flow, ws.applied)
		}
	}
}

func TestInstallFlowLeavesAnExistingBoardAlone(t *testing.T) {
	for _, flow := range []string{ThuluFlow, MinimalFlow} {
		for _, res := range []string{"keep", "update"} {
			ws := &fakeWorkshop{resolution: res}
			board, installed, err := ws.start(t).InstallFlow(context.Background(), flow)
			if err != nil || installed || board != flowBoards[flow] {
				t.Errorf("%s %s: board=%q installed=%v err=%v", flow, res, board, installed, err)
			}
			if len(ws.applied) != 0 {
				t.Errorf("%s %s: the setup rewrote a board that already exists", flow, res)
			}
		}
	}
}

func TestInstallFlowRefusesABlockedPlan(t *testing.T) {
	ws := &fakeWorkshop{resolution: "install", blocked: true}
	if _, _, err := ws.start(t).InstallFlow(context.Background(), MinimalFlow); err == nil {
		t.Error("a blocked plan installed")
	}
	if len(ws.applied) != 0 {
		t.Error("applied a blocked plan")
	}
}

func TestInstallFlowStepInstallsTheChosenBoard(t *testing.T) {
	ws := &fakeWorkshop{resolution: "install"}
	var out bytes.Buffer
	w := &run{out: &out, cfg: &Config{Board: MinimalFlow}}
	w.installFlow(context.Background(), ws.start(t))
	if len(ws.applied) != 1 || ws.applied[0]["name"] != MinimalFlow {
		t.Fatalf("apply bodies = %v, want the minimal start", ws.applied)
	}
	want := i18n.Text("initwizard.cli.flow_installed", "name", i18n.Text("initwizard.labels.board_minimal"), "board", "thulu_minimal_2")
	if !strings.Contains(out.String(), want) {
		t.Errorf("output %q does not say %q", out.String(), want)
	}
	if len(w.cfg.Deferred) != 0 {
		t.Errorf("deferred %v on success", deferredNames(w.cfg.Deferred))
	}
}

func TestInstallFlowStepDefersWhenTheApplyFails(t *testing.T) {
	for _, flow := range []string{ThuluFlow, MinimalFlow} {
		ws := &fakeWorkshop{resolution: "install", applyFails: true}
		w := &run{out: io.Discard, cfg: &Config{Board: flow}}
		w.installFlow(context.Background(), ws.start(t))
		if len(w.cfg.Deferred) != 1 || w.cfg.Deferred[0].Step != i18n.Text("initwizard.deferred.flow.step") {
			t.Fatalf("%s: a failed install was not deferred: %v", flow, deferredNames(w.cfg.Deferred))
		}
		if !strings.Contains(w.cfg.Deferred[0].Finish, workshopName(flow)) {
			t.Errorf("%s: the deferred step does not name the flow to install: %q", flow, w.cfg.Deferred[0].Finish)
		}
	}
}

func TestInstallFlowStepSkipsWhenTheBoardsStay(t *testing.T) {
	for _, cfg := range []*Config{{}, {Board: ThuluFlow, KeepBoard: true}} {
		ws := &fakeWorkshop{resolution: "install"}
		w := &run{out: io.Discard, cfg: cfg}
		if w.needsAPI() {
			t.Errorf("%+v: a run that installs nothing still needs the API", cfg)
		}
		w.installFlow(context.Background(), ws.start(t))
		if len(ws.applied) != 0 {
			t.Errorf("%+v: installed a flow on a run that leaves the boards alone", cfg)
		}
	}
}

func TestPostBootWithoutKeyDefersOnlyTheFlow(t *testing.T) {
	w := &run{out: io.Discard, cfg: &Config{Board: MinimalFlow}}
	w.postBoot(context.Background())
	names := deferredNames(w.cfg.Deferred)
	if !slices.Contains(names, i18n.Text("initwizard.deferred.flow.step")) {
		t.Errorf("deferred = %v, missing the flow", names)
	}
	if slices.Contains(names, i18n.Text("initwizard.deferred.git_and_llm.step")) {
		t.Errorf("deferred = %v: lists credentials the operator never gave", names)
	}
}

func TestBoardReviewRowAndNextSteps(t *testing.T) {
	for _, c := range []struct {
		cfg       Config
		row       string
		nextAgent bool
	}{
		{Config{Board: ThuluFlow}, "initwizard.review.board_flow", false},
		{Config{Board: MinimalFlow}, "initwizard.review.board_minimal", false},
		{Config{Board: MinimalFlow, KeepBoard: true}, "initwizard.review.board_keep", false},
		{Config{KeepBoard: true}, "initwizard.review.board_keep", true},
	} {
		if got := flowRow(&c.cfg); got != i18n.Text(c.row) {
			t.Errorf("%+v: review row %q, want %q", c.cfg, got, i18n.Text(c.row))
		}
		var out bytes.Buffer
		w := &run{out: &out, p: NewTestPrompter(strings.NewReader(""), &out), cfg: &c.cfg}
		w.nextSteps()
		if got := strings.Contains(out.String(), i18n.Text("initwizard.summary.next_agent")); got != c.nextAgent {
			t.Errorf("%+v: next steps ask to create an agent = %v, want %v", c.cfg, got, c.nextAgent)
		}
	}
}
