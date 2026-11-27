package hostrun

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// procInfo is one process as this machine's process table shows it.
type procInfo struct {
	PID, PPID, PGID int
	// The session; Linux only, 0 where the table does not say.
	SID int
	// When the process started, in the table's own unit: microseconds since the epoch on macOS,
	// clock ticks since boot on Linux. Only comparisons between two values of one machine mean
	// anything.
	Start  int64
	Comm   string
	Zombie bool
}

// procKey names one process for its whole life: a pid comes back for another process, a pid and a
// start time do not.
type procKey struct {
	PID   int
	Start int64
}

func (p procInfo) key() procKey { return procKey{PID: p.PID, Start: p.Start} }

// procDetail is what ps says about a process before the runner stops it.
type procDetail struct {
	RSSKB   int64
	Command string
}

// procOS is how the runner reads and signals this machine's processes; tests swap it for a fake
// table.
type procOS struct {
	// Every process of this user.
	list func() ([]procInfo, error)
	// One process by pid, any user; false when there is none.
	lookup func(pid int) (procInfo, bool)
	// A process's environment. A secondary signal only: a node process that sets process.title can
	// overwrite the memory it is read from, and macOS does not show the environment of its own
	// platform binaries (/bin/sleep). The process tree is the first signal.
	env func(pid int) ([]string, error)
	// Signals pid, or the group -pid; a process already gone is not an error.
	signal func(pid int, sig syscall.Signal) error
	// The memory and command line of every process, and the TCP ports each one listens on.
	details func(ctx context.Context) (map[int]procDetail, map[int][]string)
	// The working directory of each of pids that has one the runner may read; nil when none.
	cwds func(ctx context.Context, pids []int) map[int]string
	// Starts telling a tracker when a process it watches forks; nil, or errors.ErrUnsupported, where
	// the poll alone has to do.
	forks func() (forkWatch, error)
	// When the machine booted; false when unknown. Nothing a run started before it can still run,
	// and its pids may belong to anyone now.
	boot func() (time.Time, bool)
	// A wall-clock time in Start's unit.
	startAt func(time.Time) int64
}

// forkWatch wakes a run's tracker when one of the processes it watches forks, so it reads the table
// while a short-lived parent still ties the child to the run.
type forkWatch interface {
	// Receives once for any number of forks since the last receive.
	events() <-chan struct{}
	// Watches members it does not watch yet and forgets the rest; returns how many it added.
	sync(members []procInfo) int
	close()
}

var hostProcs = procOS{list: listProcs, lookup: lookupProc, env: procEnv, signal: signalPid, details: procDetails,
	cwds: procCwds, forks: newForkWatch, boot: machineBootTime, startAt: startUnits}

// parseProcArgs reads the buffer macOS returns for kern.procargs2: argc as an int32, the executable's
// path, NUL padding, argc arguments, then the environment up to an empty string.
func parseProcArgs(b []byte) (argv, env []string, err error) {
	var argc int32
	if len(b) < 4 {
		return nil, nil, apperr.Invalid("hostrun.errors.procargs")
	}
	if rerr := binary.Read(bytes.NewReader(b[:4]), binary.NativeEndian, &argc); rerr != nil {
		return nil, nil, apperr.Wrap(rerr, "hostrun.errors.procargs")
	}
	rest := b[4:]
	// The executable's path, then the padding that aligns argv.
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, nil, apperr.Invalid("hostrun.errors.procargs")
	}
	rest = bytes.TrimLeft(rest[i:], "\x00")
	fields := bytes.Split(rest, []byte{0})
	n := 0
	for ; n < len(fields) && n < int(argc); n++ {
		argv = append(argv, string(fields[n]))
	}
	for _, f := range fields[n:] {
		if len(f) == 0 {
			break
		}
		env = append(env, string(f))
	}
	return argv, env, nil
}

// parseProcStat reads /proc/<pid>/stat. The command sits in parentheses and may itself hold spaces
// and parentheses, so the fields count from the last ')': state (3), ppid (4), pgrp (5), session (6),
// starttime (22).
func parseProcStat(b []byte) (procInfo, error) {
	s := string(b)
	open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || closing < open {
		return procInfo{}, apperr.Invalid("hostrun.errors.proc_stat")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return procInfo{}, apperr.Wrap(err, "hostrun.errors.proc_stat")
	}
	f := strings.Fields(s[closing+1:])
	// f[0] is field 3.
	const state, ppid, pgrp, session, start = 0, 1, 2, 3, 19
	if len(f) <= start {
		return procInfo{}, apperr.Invalid("hostrun.errors.proc_stat")
	}
	p := procInfo{PID: pid, Comm: s[open+1 : closing], Zombie: f[state] == "Z" || f[state] == "X"}
	nums := []*int{&p.PPID, &p.PGID, &p.SID}
	for i, idx := range []int{ppid, pgrp, session} {
		if *nums[i], err = strconv.Atoi(f[idx]); err != nil {
			return procInfo{}, apperr.Wrap(err, "hostrun.errors.proc_stat")
		}
	}
	if p.Start, err = strconv.ParseInt(f[start], 10, 64); err != nil {
		return procInfo{}, apperr.Wrap(err, "hostrun.errors.proc_stat")
	}
	return p, nil
}

// parsePS reads `ps -A -o pid=,rss=,command=`.
func parsePS(out []byte) map[int]procDetail {
	m := map[int]procDetail{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, perr := strconv.Atoi(f[0])
		rss, rerr := strconv.ParseInt(f[1], 10, 64)
		if perr != nil || rerr != nil {
			continue
		}
		m[pid] = procDetail{RSSKB: rss, Command: strings.Join(f[2:], " ")}
	}
	return m
}

// parseLsof reads `lsof -Fpn`: a "p<pid>" line, then one "n<address>" line per socket.
func parseLsof(out []byte) map[int][]string {
	m := map[int][]string{}
	pid := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid = 0
			if n, err := strconv.Atoi(line[1:]); err == nil {
				pid = n
			}
		case 'n':
			if pid > 0 {
				m[pid] = append(m[pid], line[1:])
			}
		}
	}
	return m
}

// envValue is key's value in env; empty when env does not hold it.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// envHas tells whether env holds key=value exactly.
func envHas(env []string, key, value string) bool {
	want := key + "=" + value
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// parsePSRSS reads `ps -o pid=,rss=`: the resident size of each process, in KB.
func parsePSRSS(out []byte) map[int]int64 {
	m := map[int]int64{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, perr := strconv.Atoi(f[0])
		kb, kerr := strconv.ParseInt(f[1], 10, 64)
		if perr == nil && kerr == nil {
			m[pid] = kb
		}
	}
	return m
}

// parseTopMem reads `top -l 1 -stats pid,mem` on macOS: after the header lines, one row per process
// with its footprint as "371M", "1024K", "5.1G" or "12B", maybe followed by + or -. Returns KB.
func parseTopMem(out []byte) map[int]int64 {
	m := map[int]int64{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		if kb, ok := parseSize(strings.TrimRight(f[1], "+-")); ok {
			m[pid] = kb
		}
	}
	return m
}

// parseSize reads a size with a one-letter unit (B, K, M, G or T) into KB.
func parseSize(s string) (int64, bool) {
	if len(s) < 2 {
		return 0, false
	}
	n, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || n < 0 {
		return 0, false
	}
	switch s[len(s)-1] {
	case 'B':
		n /= 1024
	case 'K':
	case 'M':
		n *= 1024
	case 'G':
		n *= 1024 * 1024
	case 'T':
		n *= 1024 * 1024 * 1024
	default:
		return 0, false
	}
	return int64(n), true
}

// parseMeminfo reads key's value, in kB, from /proc/meminfo or /proc/<pid>/status.
func parseMeminfo(b []byte, key string) (int64, bool) {
	for line := range strings.SplitSeq(string(b), "\n") {
		k, rest, ok := strings.Cut(line, ":")
		if !ok || k != key {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		return kb, err == nil
	}
	return 0, false
}
