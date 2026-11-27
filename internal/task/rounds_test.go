package task

import (
	"testing"
	"time"
)

func enteredRounds(t *testing.T, store *Store, taskID string) []int {
	t.Helper()
	var out []int
	for _, p := range eventsOf(t, store, taskID, EventStageEntered) {
		n, ok := p["round"].(float64)
		if !ok {
			t.Fatalf("stage_entered without round: %v", p)
		}
		out = append(out, int(n))
	}
	return out
}

// A new round starts each time the task enters a stage earlier on the board than the one it left;
// moving forward, or redoing the same stage, stays in the round.
func TestStageEnteredCarriesItsRound(t *testing.T) {
	_, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "intake", "")
	moves := [][2]string{
		{"intake", "spec"}, {"spec", "code"}, {"code", "verify"},
		{"verify", "code"}, // send-back: round 2
		{"code", "verify"}, {"verify", "verify"}, {"verify", "review"},
		{"review", "spec"}, // manual move back: round 3
		{"spec", "gone"},   // a stage the board no longer has never starts a round
	}
	for _, m := range moves {
		if err := store.RecordEvent(t.Context(), tk.ID, EventStageEntered, "", "user", m[0]+" → "+m[1],
			map[string]string{"from": m[0], "to": m[1]}); err != nil {
			t.Fatal(err)
		}
	}
	got := enteredRounds(t, store, tk.ID)
	want := []int{1, 1, 1, 2, 2, 2, 2, 3, 3}
	if len(got) != len(want) {
		t.Fatalf("rounds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rounds = %v, want %v", got, want)
		}
	}
}

// Events written before rounds existed carry none: the count replays them by the same rule.
func TestStageEnteredRoundCountsOlderEvents(t *testing.T) {
	_, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "intake", "")
	base := time.Now().UTC().Add(-time.Hour)
	for i, p := range []string{
		`{"from":"intake","to":"code"}`, `{"from":"code","to":"verify"}`, `{"from":"verify","to":"code"}`,
	} {
		if _, err := store.db.ExecContext(t.Context(), store.rb(
			`INSERT INTO workflow_events (id, task_id, kind, ref_id, actor, summary, payload, created_at, triggered_by)
			 VALUES (?,?,?,'','user','',?,?,'')`),
			mustGenerateID(), tk.ID, EventStageEntered, p, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// A task from before tasks.round has none stored either.
	if _, err := store.db.ExecContext(t.Context(), store.rb(`UPDATE tasks SET round = 0 WHERE id = ?`), tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvent(t.Context(), tk.ID, EventStageEntered, "", "user", "",
		map[string]string{"from": "code", "to": "intake"}); err != nil {
		t.Fatal(err)
	}
	evs := eventsOf(t, store, tk.ID, EventStageEntered)
	if n, _ := evs[len(evs)-1]["round"].(float64); n != 3 {
		t.Errorf("round after one old send-back and a new one = %v, want 3", evs[len(evs)-1]["round"])
	}
}
