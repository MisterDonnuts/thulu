package hostrun

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

func TestChatRootStaysOneFolderUnderChats(t *testing.T) {
	c := &Config{WorkDir: "/w"}
	for id, want := range map[string]string{
		"chat-9f_A": "/w/chats/chat-9f_A",
		"../../etc": "/w/chats/_etc",
		"a/b.c":     "/w/chats/a_b_c",
		"":          "/w/chats/_",
	} {
		if got := c.chatRoot(id); got != want {
			t.Errorf("chatRoot(%q) = %q, want %q", id, got, want)
		}
	}
}

// A chat with no turn for 30 days goes with its engine session; a recent one stays, and so does one
// taking a turn right now.
func TestSweepChatsRemovesIdleChatsAndTheirSessions(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	r := New(&Config{WorkDir: t.TempDir()}, "test")
	now := time.Now()
	chat := func(id string, idle time.Duration) (root, session string) {
		root = r.cfg.chatRoot(id)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		session = claudeProjectsDir(resolved)
		if err := os.MkdirAll(session, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(root, now.Add(-idle), now.Add(-idle)); err != nil {
			t.Fatal(err)
		}
		return root, session
	}
	oldRoot, oldSession := chat("old", chatIdleSweep+time.Hour)
	recentRoot, recentSession := chat("recent", time.Hour)
	busyRoot, _ := chat("busy", chatIdleSweep+time.Hour)
	unlock := r.lockRepo(chatLockKey(busyRoot))
	defer unlock()

	r.sweepChats(now)

	for _, gone := range []string{oldRoot, oldSession} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep: %v", gone, err)
		}
	}
	for _, kept := range []string{recentRoot, recentSession, busyRoot} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was swept: %v", kept, err)
		}
	}
	if _, ok := r.repoLocks.Load(chatLockKey(oldRoot)); ok {
		t.Error("the swept chat kept its lock")
	}
	if _, ok := r.repoLocks.Load(chatLockKey(busyRoot)); !ok {
		t.Error("the busy chat lost its lock")
	}
}

// After a turn the chat's folder keeps only what outlives it: the code, the message, the outputs and
// the scripts' own log under runs/ and output all go.
func TestCleanupChatKeepsOnlyTheChatsFolder(t *testing.T) {
	r := New(&Config{WorkDir: t.TempDir()}, "test")
	j := &job{r: r, root: r.cfg.chatRoot("chat-1"), spec: &runner.JobSpec{ID: "job1", InstanceID: "job1",
		LaunchKind: runner.LaunchKindChatTurn}}
	if err := j.prepareChat(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"projects/shop/README.md", "runs/job1/" + runner.ChatTurnOutput, "runs/chat-1.jsonl",
		"runs/chat-1/notes.md", "tmp/job1.message", "keep.txt"} {
		p := filepath.Join(j.root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(j.root, "runs", "chat-1"), filepath.Join(j.root, "output")); err != nil {
		t.Fatal(err)
	}

	j.cleanupChat()

	for _, gone := range []string{"projects", "runs", "output", "tmp/job1.message"} {
		if _, err := os.Lstat(filepath.Join(j.root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived the turn: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(j.root, "keep.txt")); err != nil {
		t.Errorf("the chat's own files went with the turn: %v", err)
	}
	if !r.repoLock(chatLockKey(j.root)).TryLock() {
		t.Error("the turn still holds the chat")
	}
}

// A turn refused before it took the chat (an engine the machine lacks) leaves the folder alone and
// logs nothing about it.
func TestCleanupChatOfATurnThatNeverStarted(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r := New(&Config{WorkDir: t.TempDir()}, "test")
	j := &job{r: r, root: r.cfg.chatRoot("chat-1"), spec: &runner.JobSpec{ID: "job1", InstanceID: "job1",
		LaunchKind: runner.LaunchKindChatTurn}}

	j.cleanupChat()

	if _, err := os.Stat(j.root); !os.IsNotExist(err) {
		t.Errorf("the chat folder exists after a turn that never started: %v", err)
	}
	if logs.Len() != 0 {
		t.Errorf("logged %q", logs.String())
	}
}
