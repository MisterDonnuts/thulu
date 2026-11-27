package hostrun

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// kqForks wakes a run's tracker the moment one of its processes forks. macOS hides the environment
// of its own binaries (sh, bash, zsh, perl, ruby, nc, tail…), so the run's marker cannot find them:
// only the tree can. Claude Code starts each Bash command as the leader of a new session, and
// `nohup /bin/sh -c '…' &` returns before a 500 ms poll sees that leader; the loop it leaves keeps
// the leader's group, which ties it to the run only if the tracker saw the leader. A read of the
// table right after the fork sees it.
type kqForks struct {
	kq     int
	wake   chan struct{}
	done   chan struct{}
	closed atomic.Bool

	mu      sync.Mutex
	watched map[procKey]bool
}

// How long one wait for events lasts, so close does not wait longer than this.
const forkWaitSlice = 100 * time.Millisecond

func newForkWatch() (forkWatch, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.fork_watch")
	}
	unix.CloseOnExec(kq)
	w := &kqForks{kq: kq, wake: make(chan struct{}, 1), done: make(chan struct{}), watched: map[procKey]bool{}}
	go w.loop()
	return w, nil
}

func (w *kqForks) events() <-chan struct{} { return w.wake }

// sync watches every process of members not watched yet, forgets the ones no longer in it, and
// returns how many it started watching.
func (w *kqForks) sync(members []procInfo) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	keep := make(map[procKey]bool, len(members))
	added := 0
	for _, m := range members {
		k := m.key()
		keep[k] = true
		if w.watched[k] || m.PID <= 1 {
			continue
		}
		var ev unix.Kevent_t
		unix.SetKevent(&ev, m.PID, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_CLEAR)
		ev.Fflags = unix.NOTE_FORK
		// ESRCH: it exited since the read, and the next read drops it. Any other error: the next sync
		// tries again, and the poll still runs.
		if _, err := unix.Kevent(w.kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
			continue
		}
		w.watched[k] = true
		added++
	}
	for k := range w.watched {
		if !keep[k] {
			delete(w.watched, k)
		}
	}
	return added
}

func (w *kqForks) loop() {
	defer close(w.done)
	events := make([]unix.Kevent_t, 64)
	for !w.closed.Load() {
		ts := unix.NsecToTimespec(int64(forkWaitSlice))
		n, err := unix.Kevent(w.kq, nil, events, &ts)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return
		}
		if n > 0 {
			select {
			case w.wake <- struct{}{}:
			default:
			}
		}
	}
}

func (w *kqForks) close() {
	w.closed.Store(true)
	<-w.done
	logx.Warn(unix.Close(w.kq), "hostrun.log.fork_watch_close_failed")
}
