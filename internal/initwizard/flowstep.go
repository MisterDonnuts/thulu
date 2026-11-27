package initwizard

import (
	"context"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// The Workshop flows the setup offers as the workspace's first board. Each installs through the
// Workshop like any other flow, so it can also be installed, updated or removed from there later.
const (
	// A board from backlog to release, with its agents and skills.
	ThuluFlow = "thulu"
	// The factory board's four columns with one generic agent in In progress; Review is for a
	// person.
	MinimalFlow = "thulu-minimal"
)

// boardKeep is the rerun answer that installs nothing and touches no board.
const boardKeep = "keep"

// A first install defaults to the Thulu flow. A rerun defaults to leaving the boards alone:
// installing a flow makes its board the default and moves where the workspace's new tasks land.
func (iv *Interviewer) askFlow() error {
	iv.section(i18n.Text("initwizard.sections.flow.title"), i18n.Text("initwizard.sections.flow.blurb"))
	opts := []Option{
		{Value: ThuluFlow, Label: i18n.Text("initwizard.options.board_thulu.label"),
			Detail: i18n.Text("initwizard.options.board_thulu.detail")},
		{Value: MinimalFlow, Label: i18n.Text("initwizard.options.board_minimal.label"),
			Detail: i18n.Text("initwizard.options.board_minimal.detail"), Diagram: minimalBoardDiagram()},
	}
	def := 0
	if iv.Rerun {
		opts = append(opts, Option{Value: boardKeep, Label: i18n.Text("initwizard.options.board_keep.label"),
			Detail: i18n.Text("initwizard.options.board_keep.detail")})
		def = len(opts) - 1
	}
	choice, err := iv.P.Choice(i18n.Text("initwizard.prompts.board"), opts, def)
	if err != nil {
		return err
	}
	if choice == boardKeep {
		// Remember what the install has, so the next rerun still knows.
		iv.Cfg.Board, iv.Cfg.KeepBoard = iv.PreviousBoard, true
		return nil
	}
	iv.Cfg.Board, iv.Cfg.KeepBoard = choice, false
	return nil
}

func minimalBoardDiagram() string {
	return flow(i18n.Text("stage.labels.backlog"),
		i18n.Text("initwizard.diagram.board_agent", "column", i18n.Text("stage.labels.in_progress")),
		i18n.Text("initwizard.diagram.board_person", "column", i18n.Text("stage.labels.review")),
		i18n.Text("stage.labels.done"))
}

// installsBoard reports whether this run installs a flow once the daemon answers.
func (c *Config) installsBoard() bool {
	return c.Board != "" && !c.KeepBoard
}

type flowRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type flowPlan struct {
	Blocked bool `json:"blocked"`
	Items   []struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Resolution string `json:"resolution"`
		Target     string `json:"target"`
	} `json:"items"`
}

func (p *flowPlan) board(name string) (target, resolution string) {
	for _, it := range p.Items {
		if it.Kind == "flow" && it.Name == name {
			return it.Target, it.Resolution
		}
	}
	return "", ""
}

// InstallFlow installs the Workshop flow name and makes its board the default. It only creates:
// when the workspace already has the flow's board, it leaves it alone and returns installed false,
// so rerunning the setup never rewrites a board someone uses. The board the workspace had before
// stays too, with its tasks: it only stops being the default.
func (c *APIClient) InstallFlow(ctx context.Context, name string) (board string, installed bool, err error) {
	items := []flowRef{{Kind: "flow", Name: name}}
	var plan flowPlan
	if err := c.do(ctx, http.MethodPost, "/workshop/plan", map[string]any{"items": items}, &plan); err != nil {
		return "", false, err
	}
	board, resolution := plan.board(name)
	switch {
	case resolution == "":
		return "", false, apperr.Invalid("initwizard.errors.flow_missing", "flow", name)
	case resolution != "install":
		return board, false, nil
	case plan.Blocked:
		return board, false, apperr.Invalid("initwizard.errors.flow_blocked", "flow", name)
	}
	var applied struct {
		Plan flowPlan `json:"plan"`
	}
	body := map[string]any{"items": items, "flow_default": true}
	if err := c.do(ctx, http.MethodPost, "/workshop/apply", body, &applied); err != nil {
		return board, false, err
	}
	if target, _ := applied.Plan.board(name); target != "" {
		board = target
	}
	return board, true, nil
}

func (w *run) installFlow(ctx context.Context, client *APIClient) {
	if !w.cfg.installsBoard() {
		return
	}
	name := boardName(w.cfg.Board)
	board, installed, err := client.InstallFlow(ctx, w.cfg.Board)
	switch {
	case err != nil:
		w.warn(i18n.Text("initwizard.cli.flow_failed", "name", name, "err", apperr.Text(err)))
		w.deferFlow()
	case installed:
		w.done(i18n.Text("initwizard.cli.flow_installed", "name", name, "board", board))
	default:
		w.say("     ", "initwizard.cli.flow_present", "board", board)
	}
}

func (w *run) deferFlow() {
	w.cfg.Defer(i18n.Text("initwizard.deferred.flow.step"),
		i18n.Text("initwizard.deferred.flow.effect"),
		i18n.Text("initwizard.deferred.flow.finish", "flow", workshopName(w.cfg.Board)))
}

// boardName names a flow inside a sentence: "the Thulu flow", "the minimal start".
func boardName(flow string) string {
	if flow == MinimalFlow {
		return i18n.Text("initwizard.labels.board_minimal")
	}
	return i18n.Text("initwizard.labels.board_thulu")
}

// workshopName is the flow's title as the Workshop lists it.
func workshopName(flow string) string {
	if flow == MinimalFlow {
		return i18n.Text("initwizard.labels.board_minimal_workshop")
	}
	return i18n.Text("initwizard.labels.board_thulu_workshop")
}

func flowRow(c *Config) string {
	switch {
	case !c.installsBoard():
		return i18n.Text("initwizard.review.board_keep")
	case c.Board == MinimalFlow:
		return i18n.Text("initwizard.review.board_minimal")
	}
	return i18n.Text("initwizard.review.board_flow")
}
