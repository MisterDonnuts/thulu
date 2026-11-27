//go:build unix

package hostrun

import (
	"context"
	"fmt"
	"io"
	"log"
	"strconv"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// How long a run's processes get to exit on SIGTERM before the runner stops and kills them.
var termWait = 5 * time.Second

const (
	killRounds = 5
	killPause  = 200 * time.Millisecond
	killPoll   = 100 * time.Millisecond
	// A command line in the log stops here.
	commandMax = 200
)

// killRun ends every process the run started, in its process group or out of it, and returns the
// ones still running after it gave up. It refreshes the set first (one more read of the tree, then
// every process started after the run whose environment names it), logs what it is about to stop
// and the ports those processes listen on, sends SIGTERM, waits up to termWait, then runs rounds of
// SIGSTOP and SIGKILL until none is left. Each line goes to the runner's log and to out, the run's
// own log.
func killRun(ctx context.Context, t *runTracker, out io.Writer) []procInfo {
	t.refresh(true)
	members := t.Members()
	if len(members) == 0 {
		t.save()
		return nil
	}
	details, ports := t.procs.details(ctx)
	for _, m := range members {
		t.note(out, "hostrun.log.kill_member", "job", t.job, "pid", m.PID, "rss", rssMB(details, m.PID),
			"command", commandOf(details, m))
		for _, addr := range ports[m.PID] {
			t.note(out, "hostrun.log.kill_listener", "job", t.job, "pid", m.PID, "command", commandOf(details, m), "addr", addr)
		}
	}
	t.signalAll(members, syscall.SIGTERM)
	left := t.waitGone(termWait)
	for round := 0; round < killRounds && len(left) > 0; round++ {
		// Children forked meanwhile join first; STOP keeps the rest from forking more before KILL.
		t.refresh(true)
		left = t.Members()
		t.signalAll(left, syscall.SIGSTOP)
		t.signalAll(left, syscall.SIGKILL)
		time.Sleep(killPause)
		t.refresh(false)
		left = t.Members()
	}
	t.reportEnd(out, members, left, details, ports)
	t.save()
	return left
}

func (t *runTracker) reportEnd(out io.Writer, members, left []procInfo, details map[int]procDetail, ports map[int][]string) {
	survived := map[procKey]bool{}
	for _, s := range left {
		survived[s.key()] = true
		t.note(out, "hostrun.log.kill_survivor", "job", t.job, "pid", s.PID, "rss", rssMB(details, s.PID),
			"command", commandOf(details, s))
	}
	for _, m := range members {
		if survived[m.key()] {
			continue
		}
		for _, addr := range ports[m.PID] {
			t.note(out, "hostrun.log.port_closed", "job", t.job, "addr", addr, "command", commandOf(details, m), "pid", m.PID)
		}
	}
	t.note(out, "hostrun.log.kill_done", "job", t.job, "count", len(members)-len(left), "left", len(left))
}

func (t *runTracker) note(out io.Writer, key string, args ...any) {
	line := i18n.Log(key, args...)
	log.Printf("[runner] %s", line)
	if out != nil {
		if _, err := fmt.Fprintf(out, "[runner] %s\n", line); err != nil {
			logx.Warn(err, "hostrun.log.kill_note_failed", "job", t.job)
		}
	}
}

// waitGone polls until no member runs or d passes, and returns the ones left.
func (t *runTracker) waitGone(d time.Duration) []procInfo {
	deadline := time.Now().Add(d)
	for {
		t.refresh(false)
		left := t.Members()
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(killPoll)
	}
}

// signalAll signals every member and, for a member that leads its group, the whole group: a child
// forked since the last read is in it.
func (t *runTracker) signalAll(members []procInfo, sig syscall.Signal) {
	for _, m := range members {
		t.signal(m, sig, false)
		if m.PGID == m.PID {
			t.signal(m, sig, true)
		}
	}
}

// signal sends sig to p, or to the group p leads, only while p's pid still belongs to p: same start
// time, not a zombie. It never reaches pid 1, the runner or the runner's own group.
func (t *runTracker) signal(p procInfo, sig syscall.Signal, group bool) {
	if p.PID <= 1 || p.PID == t.self || p.PID == t.selfGroup {
		return
	}
	cur, ok := t.procs.lookup(p.PID)
	if !ok || cur.Start != p.Start || cur.Zombie || (group && cur.PGID != cur.PID) {
		return
	}
	if group {
		logx.Warn(t.procs.signal(-p.PID, sig), "hostrun.log.signal_failed", "pid", p.PID, "signal", sig.String())
		return
	}
	logx.Warn(t.procs.signal(p.PID, sig), "hostrun.log.signal_pid_failed", "pid", p.PID, "signal", sig.String())
}

func commandOf(details map[int]procDetail, p procInfo) string {
	c := p.Comm
	if d, ok := details[p.PID]; ok && d.Command != "" {
		c = d.Command
	}
	c = logx.Safe(c)
	if len(c) > commandMax {
		c = c[:commandMax] + "…"
	}
	return c
}

func rssMB(details map[int]procDetail, pid int) string {
	d, ok := details[pid]
	if !ok {
		return "?"
	}
	return strconv.FormatFloat(float64(d.RSSKB)/1024, 'f', 1, 64)
}
