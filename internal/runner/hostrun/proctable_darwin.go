package hostrun

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// The process table is readable here: a runner may take runs.
const procTableSupported = true

// macOS's p_stat for a process that exited and waits for its parent to reap it.
const darwinZombie = 5

func listProcs() ([]procInfo, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.proc_list")
	}
	out := make([]procInfo, 0, len(kps))
	for i := range kps {
		out = append(out, fromKinfo(&kps[i]))
	}
	return out, nil
}

func lookupProc(pid int) (procInfo, bool) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil || len(kps) == 0 || int(kps[0].Proc.P_pid) != pid {
		return procInfo{}, false
	}
	return fromKinfo(&kps[0]), true
}

func fromKinfo(kp *unix.KinfoProc) procInfo {
	t := kp.Proc.P_starttime
	return procInfo{
		PID:    int(kp.Proc.P_pid),
		PPID:   int(kp.Eproc.Ppid),
		PGID:   int(kp.Eproc.Pgid),
		Start:  t.Sec*1_000_000 + int64(t.Usec),
		Comm:   unix.ByteSliceToString(kp.Proc.P_comm[:]),
		Zombie: kp.Proc.P_stat == darwinZombie,
	}
}

// procEnv reads kern.procargs2, which holds a process's arguments and environment as it was
// started. See procOS.env for what it cannot see.
func procEnv(pid int) ([]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.proc_env", "pid", pid)
	}
	_, env, err := parseProcArgs(b)
	return env, err
}

// procCwds asks lsof once for the working directory of every pid: macOS has no /proc, and
// proc_pidinfo needs cgo.
func procCwds(ctx context.Context, pids []int) map[int]string {
	if len(pids) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, detailsTimeout)
	defer cancel()
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	args := []string{"-a", "-d", "cwd", "-Fn", "-p", strings.Join(list, ",")}
	out, err := exec.CommandContext(ctx, "lsof", args...).Output() //nolint:gosec // G204: fixed flags and numeric pids
	// lsof exits 1 when one of the pids is gone; what it printed still holds for the rest.
	if err != nil && len(out) == 0 {
		return nil
	}
	cwds := map[int]string{}
	for pid, names := range parseLsof(out) {
		if len(names) > 0 {
			cwds[pid] = names[0]
		}
	}
	return cwds
}

// machineBootTime reads kern.boottime.
func machineBootTime() (time.Time, bool) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil || tv.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), true
}

// startUnits is t in procInfo.Start's unit here: microseconds since the epoch.
func startUnits(t time.Time) int64 { return t.UnixMicro() }
