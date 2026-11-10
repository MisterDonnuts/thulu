package task

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestMentionAnswersInItsThread(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	ctx := context.Background()
	tk := &Task{WorkspaceID: testWS, Title: "threads"}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	root, _ := store.AddComment(ctx, tk.ID, SourceUser, "p@x", "why does the deploy fail?")
	ask, _ := store.AddReply(ctx, tk.ID, root.ID, SourceUser, "p@x", "@devops look at this")
	full, _ := store.Get(ctx, tk.ID)

	d.HandleMention(ctx, full, mentionAgent(), ask)
	launched := l.launches()
	if len(launched) != 1 || launched[0]["THREAD_ID"] != root.ID {
		t.Fatalf("launches = %v, want one run on thread %s", launched, root.ID)
	}
	gotTask, gotThread, ok := parseMentionInstanceID(launched[0]["INSTANCE_ID"])
	if !ok || gotTask != tk.ID || gotThread != root.ID {
		t.Errorf("instance %q parses as %q/%q/%v", launched[0]["INSTANCE_ID"], gotTask, gotThread, ok)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "comment.md"), []byte("falta la variable"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.exitIn(t, "ctr-a", dir)

	comments, _ := store.ListComments(ctx, tk.ID)
	var reply bool
	for _, c := range comments {
		if c.ID != root.ID && c.ParentID != root.ID {
			t.Errorf("%s comment %q landed outside the thread", c.Source, c.Body)
		}
		if c.Source == SourceAgent && c.Author == "devops" && c.Body == "falta la variable" {
			reply = true
		}
	}
	if !reply {
		t.Errorf("no reply from devops in the thread: %+v", comments)
	}
}

func TestOneAgentAnswersTwoThreadsAtOnce(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	ctx := context.Background()
	tk := &Task{WorkspaceID: testWS, Title: "threads"}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	a, _ := store.AddComment(ctx, tk.ID, SourceUser, "p@x", "@devops tema A")
	b, _ := store.AddComment(ctx, tk.ID, SourceUser, "p@x", "@devops tema B")
	full, _ := store.Get(ctx, tk.ID)

	d.HandleMention(ctx, full, mentionAgent(), a)
	d.HandleMention(ctx, full, mentionAgent(), b)
	if got := len(l.launches()); got != 2 {
		t.Fatalf("launched %d runs for two threads, want 2", got)
	}
}

func TestChatAgentMentionsQueuePerThread(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	ctx := context.Background()
	tk := &Task{WorkspaceID: testWS, Title: "threads"}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	root, _ := store.AddComment(ctx, tk.ID, SourceUser, "p@x", "@manager primera")
	second, _ := store.AddReply(ctx, tk.ID, root.ID, SourceUser, "p@x", "@manager segunda")
	full, _ := store.Get(ctx, tk.ID)
	manager := &types.AgentType{Name: "manager", Type: "manager", ValidStages: []types.Stage{types.StageChat}}

	answered := make(chan string, 4)
	release := make(chan struct{})
	d.WithChatMentions(func(_ context.Context, _ *Task, _ *types.AgentType, c *Comment) {
		answered <- c.Body
		<-release
	})

	go d.HandleMention(ctx, full, manager, root)
	if got := <-answered; got != "@manager primera" {
		t.Fatalf("first turn answered %q", got)
	}
	d.HandleMention(ctx, full, manager, second)
	close(release)
	if got := <-answered; got != "@manager segunda" {
		t.Errorf("follow-up answered %q, want the queued message", got)
	}
	if n := len(l.launches()); n != 0 {
		t.Errorf("a chat agent launched %d mention containers", n)
	}
	comments, _ := store.ListComments(ctx, tk.ID)
	for _, c := range comments {
		if c.Source == SourceSystem && c.ParentID != root.ID {
			t.Errorf("notice %q landed outside the thread", c.Body)
		}
	}
}

func TestMentionReadsTheCodeWithoutAWorktree(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	_, store, l := newMentionDispatcher(t)
	projects, err := project.NewStore(store.db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	proj, err := projects.Create(ctx, "web", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	d := NewDispatcher(store, l, projects, nil, nil)
	tk := &Task{WorkspaceID: testWS, Title: "backlog", ProjectIDs: []string{proj.ID}}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	ask, _ := store.AddComment(ctx, tk.ID, SourceUser, "p@x", "@devops where is the deploy built?")
	full, _ := store.Get(ctx, tk.ID)

	d.HandleMention(ctx, full, mentionAgent(), ask)
	name := filepath.Base(proj.Path)
	if len(l.code) != 1 || l.code[0][name] != proj.Path || !l.ro[0] {
		t.Fatalf("mounts = %v read-only %v, want the checkout of %s read-only", l.code, l.ro, name)
	}
	if got := full.BranchAt(proj.Path); got != "" {
		t.Errorf("branch at the checkout = %q, want the default", got)
	}

	full.WorktreePaths = map[string]string{proj.ID: "/data/projects/web--task-1"}
	full.BranchName = "task-1"
	view := ReadOnlyCode(ctx, full, projects)[name]
	if view.Path != "/data/projects/web--task-1" || view.Branch != "task-1" {
		t.Errorf("with a worktree the view is %+v", view)
	}
	if got := full.BranchAt(view.Path); got != "task-1" {
		t.Errorf("branch at the worktree = %q", got)
	}
}

func TestAgentRepliesReachTheMentionHook(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	var seen []*Comment
	d.WithAgentComments(func(_ context.Context, _ string, c *Comment) { seen = append(seen, c) })
	ctx := context.Background()
	tk := &Task{WorkspaceID: testWS, Title: "hooks"}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	ask, _ := store.AddComment(ctx, tk.ID, SourceUser, "p@x", "@devops what about the deploy?")
	full, _ := store.Get(ctx, tk.ID)
	d.HandleMention(ctx, full, mentionAgent(), ask)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "comment.md"), []byte("@developer look at the Dockerfile"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.exitIn(t, "ctr-a", dir)
	if len(seen) != 1 || seen[0].Author != "devops" || seen[0].ParentID != ask.ID {
		t.Fatalf("hook saw %+v, want devops's reply in the thread", seen)
	}

	d.postRunArtifacts(ctx, tk.ID, "developer", "developer-task-"+tk.ID+"-in_progress-1", dir)
	if len(seen) != 2 || seen[1].Author != "developer" || seen[1].ParentID != "" {
		t.Errorf("hook saw %+v, want the stage run's comment too", seen)
	}
}
