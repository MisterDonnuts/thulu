package hostrun

import (
	"context"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// The process table is readable here: a runner may take runs.
const procTableSupported = true

var procFS = os.DirFS("/proc")

func listProcs() ([]procInfo, error) {
	entries, err := fs.ReadDir(procFS, ".")
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.proc_list")
	}
	uid := int64(os.Getuid())
	var out []procInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int64(st.Uid) != uid {
			continue
		}
		if p, ok := lookupProc(pid); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func lookupProc(pid int) (procInfo, bool) {
	b, err := fs.ReadFile(procFS, strconv.Itoa(pid)+"/stat")
	if err != nil {
		return procInfo{}, false
	}
	p, err := parseProcStat(b)
	if err != nil || p.PID != pid {
		return procInfo{}, false
	}
	return p, true
}

// procEnv reads /proc/<pid>/environ, the environment the process started with. See procOS.env for
// what it cannot see.
func procEnv(pid int) ([]string, error) {
	b, err := fs.ReadFile(procFS, strconv.Itoa(pid)+"/environ")
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.proc_env", "pid", pid)
	}
	var env []string
	for kv := range strings.SplitSeq(string(b), "\x00") {
		if kv != "" {
			env = append(env, kv)
		}
	}
	return env, nil
}

// procCwds reads /proc/<pid>/cwd of each pid.
func procCwds(_ context.Context, pids []int) map[int]string {
	cwds := map[int]string{}
	for _, pid := range pids {
		if dir, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil {
			cwds[pid] = dir
		}
	}
	return cwds
}

// machineBootTime reads btime, the boot time in seconds since the epoch, from /proc/stat.
func machineBootTime() (time.Time, bool) {
	b, err := fs.ReadFile(procFS, "stat")
	if err != nil {
		return time.Time{}, false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			if s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && s > 0 {
				return time.Unix(s, 0), true
			}
		}
	}
	return time.Time{}, false
}

// USER_HZ, the unit of /proc/<pid>/stat's starttime: 100 on every Linux the runner supports.
const clockTicks = 100

// startUnits is t in procInfo.Start's unit here: clock ticks since boot; 0 before it.
func startUnits(t time.Time) int64 {
	boot, ok := machineBootTime()
	if !ok || !t.After(boot) {
		return 0
	}
	return int64(t.Sub(boot) / (time.Second / clockTicks))
}
