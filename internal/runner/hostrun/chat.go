package hostrun

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// A chat turn runs in the chat's own folder, kept between turns: the engine finds its session by the
// folder it runs in. Each turn still gets fresh code and its own outputs folder.

// The file the entrypoint feeds chat-turn.sh as stdin; nothing between the runner and the script may
// eat the message.
const chatInputEnv = "CHAT_INPUT_FILE"

// The daemon tells a machine nothing when a chat is deleted, so a chat folder (and its session) goes
// after this long without a turn.
const chatIdleSweep = 30 * 24 * time.Hour

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func (c *Config) chatsDir() string { return filepath.Join(c.WorkDir, "chats") }

// Dots and separators never survive, so whatever id the daemon sends stays one folder under chats.
func (c *Config) chatRoot(chatID string) string {
	name := unsafeName.ReplaceAllString(chatID, "_")
	if name == "" {
		name = "_"
	}
	return filepath.Join(c.chatsDir(), name)
}

func chatLockKey(root string) string { return "chat:" + filepath.Base(root) }

// prepareChat holds the chat for the whole job (cleanup lets it go) and clears what the last turn may
// have left after a crash: an engine still writing the session, and the code, laid out again on every
// turn.
func (j *job) prepareChat(ctx context.Context) error {
	j.unlock = j.r.lockRepo(chatLockKey(j.root))
	j.r.killChatLeftovers(ctx, j.root)
	if err := os.MkdirAll(j.root, 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	touch(j.root)
	_, statErr := os.Stat(filepath.Join(j.root, "ro"))
	for _, dir := range []string{"projects", "ro"} {
		if err := os.RemoveAll(filepath.Join(j.root, dir)); err != nil {
			return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
		}
	}
	// A turn that died left its read-only worktrees registered in the mirrors.
	if statErr == nil {
		j.pruneProjectMirrors(ctx)
	}
	return nil
}

func (j *job) pruneProjectMirrors(ctx context.Context) {
	for i := range j.spec.Projects {
		p := &j.spec.Projects[i]
		if p.FromDaemon() {
			continue
		}
		repo := j.r.cfg.mirrorDir(p.ID)
		if _, err := os.Stat(repo); err != nil {
			continue
		}
		unlock := j.r.lockRepo(p.ID)
		if _, err := git(ctx, repo, "worktree", "prune"); err != nil {
			logx.Warn(err, "hostrun.log.prune_failed", "repo", repo)
		}
		unlock()
	}
}

func (j *job) chatInputPath() string { return filepath.Join(j.root, "tmp", j.spec.ID+".message") }

func (j *job) turnOutputPath() string {
	return filepath.Join(j.root, "runs", j.spec.InstanceID, runner.ChatTurnOutput)
}

func (j *job) writeChatInput() error {
	if err := os.WriteFile(j.chatInputPath(), []byte(j.spec.Stdin), 0o600); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	return nil
}

// runTurn runs the turn with the engine's stdout in the turn's outputs, which the daemon reads as
// the answer; the scripts' stderr goes to the job's log, as a container exec's would.
func (j *job) runTurn(ctx context.Context, ship io.Writer) (int, error) {
	f, err := os.OpenFile(j.turnOutputPath(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return runner.ExitLost, apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	defer logx.Close(f, j.turnOutputPath())
	return j.run(ctx, f, ship)
}

// cleanupChat keeps the folder and the session, and drops what belongs to this turn alone: its code,
// its message, runs/ and output, where the turn's outputs and the scripts' own log land, the turn's
// TMPDIR and Claude Code's folder in /tmp. The session lives in ~/.claude/projects, not in either.
func (j *job) cleanupChat() {
	// prepare stopped before it took the chat: this turn left nothing in the folder.
	if j.unlock == nil {
		return
	}
	defer j.unlock()
	touch(j.root)
	// What still runs of the turn may be using these; the next turn's prepareChat clears them.
	if len(j.survivors) > 0 {
		return
	}
	for _, name := range []string{"projects", "ro", "runs", "output"} {
		p := filepath.Join(j.root, name)
		logx.Warn(os.RemoveAll(p), "hostrun.log.remove_failed", "path", p)
	}
	emptyDir(filepath.Join(j.root, "tmp"))
	if resolved, err := filepath.EvalSymlinks(j.root); err == nil {
		removeClaudeTmp(resolved)
	}
}

// emptyDir removes what dir holds and keeps dir itself.
func emptyDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		logx.Warn(os.RemoveAll(p), "hostrun.log.remove_failed", "path", p)
	}
}

func touch(path string) {
	now := time.Now()
	logx.Warn(os.Chtimes(path, now, now), "hostrun.log.touch_failed", "path", path)
}

// sweepChats removes the chats with no turn for chatIdleSweep, with their sessions. A chat taking a
// turn right now is left for the next sweep.
func (r *Runner) sweepChats(now time.Time) {
	entries, err := os.ReadDir(r.cfg.chatsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() || now.Sub(info.ModTime()) < chatIdleSweep {
			continue
		}
		root := filepath.Join(r.cfg.chatsDir(), e.Name())
		unlock, ok := r.tryLockRepo(chatLockKey(root))
		if !ok {
			continue
		}
		removeSession(root)
		if err := os.RemoveAll(root); err != nil {
			logx.Warn(err, "hostrun.log.remove_failed", "path", root)
		} else {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.chat_swept", "chat", e.Name()))
			// Else every chat ever seen keeps a lock. The daemon sends one turn of a chat at a time,
			// so no turn waits on this one.
			r.repoLocks.Delete(chatLockKey(root))
		}
		unlock()
	}
}
