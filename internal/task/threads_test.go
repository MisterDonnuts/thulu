package task

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestThreadsGroupRepliesUnderTheirRoot(t *testing.T) {
	got := Threads([]Comment{
		{ID: "a"},
		{ID: "b"},
		{ID: "a1", ParentID: "a"},
		{ID: "b1", ParentID: "b"},
		{ID: "a2", ParentID: "a"},
		{ID: "x1", ParentID: "gone"},
	})
	want := [][]string{{"a", "a1", "a2"}, {"b", "b1"}, {"x1"}}
	if len(got) != len(want) {
		t.Fatalf("threads = %+v", got)
	}
	for i, th := range got {
		ids := []string{th.Root.ID}
		for _, r := range th.Replies {
			ids = append(ids, r.ID)
		}
		if len(ids) != len(want[i]) {
			t.Fatalf("thread %d = %v, want %v", i, ids, want[i])
		}
		for j := range ids {
			if ids[j] != want[i][j] {
				t.Fatalf("thread %d = %v, want %v", i, ids, want[i])
			}
		}
	}
}

func TestAddReplyFlattensAndStaysOnItsTask(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	a := &Task{WorkspaceID: testWS, Title: "a"}
	b := &Task{WorkspaceID: testWS, Title: "b"}
	for _, tk := range []*Task{a, b} {
		if err := store.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	root, err := store.AddComment(ctx, a.ID, SourceUser, "p@x", "why does login fail?")
	if err != nil {
		t.Fatal(err)
	}
	r1, err := store.AddReply(ctx, a.ID, root.ID, SourceAgent, "developer", "falta el header")
	if err != nil || r1.ParentID != root.ID {
		t.Fatalf("reply = %+v, %v", r1, err)
	}
	r2, err := store.AddReply(ctx, a.ID, r1.ID, SourceUser, "p@x", "which one?")
	if err != nil || r2.ParentID != root.ID {
		t.Fatalf("reply to a reply = %+v, %v; want it under the root", r2, err)
	}
	if _, err := store.AddReply(ctx, b.ID, root.ID, SourceUser, "p@x", "x"); !errors.Is(err, ErrNoSuchThread) {
		t.Fatalf("reply from another task: err = %v, want ErrNoSuchThread", err)
	}
	if _, err := store.AddReply(ctx, a.ID, "nope", SourceUser, "p@x", "x"); !errors.Is(err, ErrNoSuchThread) {
		t.Fatalf("reply to nothing: err = %v, want ErrNoSuchThread", err)
	}

	cs, _ := store.ListComments(ctx, a.ID)
	threads := Threads(cs)
	if len(threads) != 1 || len(threads[0].Replies) != 2 {
		t.Fatalf("threads = %+v", threads)
	}
}

func TestForkKeepsThreads(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	src := &Task{WorkspaceID: testWS, Title: "src"}
	if err := store.Create(ctx, src); err != nil {
		t.Fatal(err)
	}
	root, _ := store.AddComment(ctx, src.ID, SourceUser, "p@x", "tema")
	if _, err := store.AddReply(ctx, src.ID, root.ID, SourceAgent, "developer", "respuesta"); err != nil {
		t.Fatal(err)
	}
	fork := &Task{WorkspaceID: testWS, Title: "fork"}
	if err := store.Create(ctx, fork); err != nil {
		t.Fatal(err)
	}
	if err := store.ForkData(ctx, src.ID, fork.ID); err != nil {
		t.Fatalf("fork: %v", err)
	}
	cs, _ := store.ListComments(ctx, fork.ID)
	threads := Threads(cs)
	if len(threads) != 1 || len(threads[0].Replies) != 1 {
		t.Fatalf("fork threads = %+v", threads)
	}
	if threads[0].Root.ID == root.ID || threads[0].Replies[0].ParentID != threads[0].Root.ID {
		t.Errorf("fork reply points at %q, root copy is %q", threads[0].Replies[0].ParentID, threads[0].Root.ID)
	}
}

func TestThreadLastAgentAndSince(t *testing.T) {
	th := Threads([]Comment{
		{ID: "r", Source: SourceUser, Author: "p@x"},
		{ID: "1", ParentID: "r", Source: SourceAgent, Author: "devops"},
		{ID: "2", ParentID: "r", Source: SourceUser, Author: "p@x"},
		{ID: "3", ParentID: "r", Source: SourceAgent, Author: "reviewer"},
		{ID: "4", ParentID: "r", Source: SourceSystem, Author: "octopus"},
		{ID: "5", ParentID: "r", Source: SourceUser, Author: "p@x"},
	})[0]
	if got := th.LastAgent(); got != "reviewer" {
		t.Errorf("LastAgent = %q, want reviewer", got)
	}
	ids := func(cs []Comment) (out []string) {
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	if got := ids(th.Since("devops")); len(got) != 4 || got[0] != "2" {
		t.Errorf("Since(devops) = %v, want 2..5", got)
	}
	if got := ids(th.Since("manager")); len(got) != 6 || got[0] != "r" {
		t.Errorf("Since(manager) = %v, want the whole thread", got)
	}
	if got := (&Comment{ID: "5", ParentID: "r"}).ThreadID(); got != "r" {
		t.Errorf("ThreadID of a reply = %q", got)
	}
	if got := (&Comment{ID: "r"}).ThreadID(); got != "r" {
		t.Errorf("ThreadID of a root = %q", got)
	}
}

func TestAddToThreadFallsBackToARoot(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := &Task{WorkspaceID: testWS, Title: "a"}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	root, err := store.AddComment(ctx, tk.ID, SourceUser, "p@x", "hola")
	if err != nil {
		t.Fatal(err)
	}
	in, err := store.AddToThread(ctx, tk.ID, root.ID, SourceAgent, "devops", "respuesta")
	if err != nil || in.ParentID != root.ID {
		t.Fatalf("AddToThread = %+v, %v", in, err)
	}
	out, err := store.AddToThread(ctx, tk.ID, "missing", SourceAgent, "devops", "igual")
	if err != nil || out.ParentID != "" {
		t.Fatalf("AddToThread(missing) = %+v, %v; want a root", out, err)
	}
}

func TestThreadAgentTurnsSincePerson(t *testing.T) {
	th := Thread{Root: Comment{ID: "r", Source: SourceAgent}, Replies: []Comment{
		{Source: SourceAgent},
		{Source: SourceUser},
		{Source: SourceAgent},
		{Source: SourceSystem},
		{Source: SourceAgent},
	}}
	if got := th.AgentTurnsSincePerson(); got != 2 {
		t.Errorf("turns since the person = %d, want 2", got)
	}
	agentsOnly := Thread{Root: Comment{Source: SourceAgent}, Replies: []Comment{{Source: SourceAgent}}}
	if got := agentsOnly.AgentTurnsSincePerson(); got != 2 {
		t.Errorf("a thread no person wrote in = %d, want 2", got)
	}
}

func TestTranscript(t *testing.T) {
	msgs := []Comment{
		{ID: "1", Source: SourceUser, Author: "p", Body: "ñandú ñandú"},
		{ID: "2", Source: SourceSystem, Author: "octopus", Body: "invocado"},
		{ID: "3", Source: SourceAgent, Author: "a", Body: "respuesta"},
		{ID: "4", Source: SourceUser, Author: "p", Body: "la pregunta"},
	}
	full := Transcript(msgs, "4", 1<<10)
	if full != "**p:** ñandú ñandú\n\n**@a (agent):** respuesta\n\n" {
		t.Errorf("transcript = %q", full)
	}
	last := "**@a (agent):** respuesta\n\n"
	for n := len(last); n < len(full); n++ {
		cut := Transcript(msgs, "4", n)
		if !utf8.ValidString(cut) || !strings.HasSuffix(cut, "**@a (agent):** respuesta\n\n") || !strings.HasPrefix(cut, "…(") {
			t.Fatalf("cut at %d = %q", n, cut)
		}
	}
}
