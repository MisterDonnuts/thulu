// Package procreap kills, inside a container, the processes a run or a chat turn left behind. It
// finds them by a variable the run's commands carry in their environment, which a detached child
// (setsid, nohup, a double fork) inherits and keeps.
package procreap

import "strings"

const (
	envMode  = "OCTOPUS_REAP_MODE"
	envVar   = "OCTOPUS_REAP_VAR"
	envValue = "OCTOPUS_REAP_VALUE"
	envKeep  = "OCTOPUS_REAP_KEEP"
)

// TimeoutMs bounds one reap: three seconds of grace, five kill rounds, and the scans between them.
const TimeoutMs = 20_000

// Script is the reaper. In "run" mode it kills the processes whose environment holds
// $OCTOPUS_REAP_VAR=$OCTOPUS_REAP_VALUE; in "any" mode those that hold $OCTOPUS_REAP_VAR with any
// value; in "all" mode every one. It spares PID 1, the container's
// main command (sleep infinity or tail -f /dev/null under the init, or the exec agent), every
// session a pid file under the $OCTOPUS_REAP_KEEP glob names, and its own ancestry. Each pid is
// checked by its start time before every signal, so a reused pid is never hit. TERM first, then
// three seconds, then STOP+KILL rounds that rescan, because a dying server can fork again. It prints
// one "reap" line per process it signals and one "survivor" line per process alive at the end. It
// needs bash and /proc.
func Script() string {
	//i18n:ignore shell script run in a container
	return `mode="$OCTOPUS_REAP_MODE"; var="$OCTOPUS_REAP_VAR"; val="$OCTOPUS_REAP_VALUE"; keepglob="$OCTOPUS_REAP_KEEP"
fld() { local s n=$2; read -r s 2>/dev/null < "/proc/$1/stat" || return 1; s=${s##*) }; set -- $s; eval "printf '%s' \"\${$n}\""; }
self=" "; p=$$
while [ -n "$p" ] && [ "$p" != 0 ]; do self="$self$p "; [ "$p" = 1 ] && break; p=$(fld "$p" 2) || break; done
keep=" "
[ -n "$keepglob" ] && for f in $keepglob; do [ -s "$f" ] && keep="$keep$(cat "$f" 2>/dev/null) "; done
cmdof() { local c=""; [ -r "/proc/$1/cmdline" ] && c=$(tr '\0' ' ' < "/proc/$1/cmdline" 2>/dev/null | cut -c1-200); printf '%s' "$c"; }
spare() {
  case "$self" in *" $1 "*) return 0;; esac
  [ "$1" = 1 ] && return 0
  local c; c=$(cmdof "$1")
  case "$c" in *sandbox-execagent*) return 0;; esac
  if [ "$(fld "$1" 2)" = 1 ]; then case "$c" in "sleep infinity "|"tail -f /dev/null ") return 0;; esac; fi
  case "$keep" in *" $(fld "$1" 4) "*) return 0;; esac
  return 1
}
match() {
  [ "$mode" = all ] && return 0
  local kv
  if [ "$mode" = any ]; then
    while IFS= read -r -d '' kv; do case "$kv" in "$var="*) return 0;; esac; done 2>/dev/null < "/proc/$1/environ"
    return 1
  fi
  while IFS= read -r -d '' kv; do [ "$kv" = "$var=$val" ] && return 0; done 2>/dev/null < "/proc/$1/environ"
  return 1
}
targets=""; seen=" "
scan() {
  targets=""
  local d pid st
  for d in /proc/[0-9]*; do
    pid=${d#/proc/}
    st=$(fld "$pid" 1) || continue
    [ "$st" = Z ] && continue
    spare "$pid" && continue
    match "$pid" || continue
    targets="$targets $pid:$(fld "$pid" 20)"
    case "$seen" in *" $pid "*) ;; *)
      seen="$seen$pid "
      echo "reap pid=$pid rss_kb=$(awk '/^VmRSS:/{print $2}' "/proc/$pid/status" 2>/dev/null) cmd=$(cmdof "$pid")";;
    esac
  done
}
same() { [ "$(fld "$1" 20)" = "$2" ] && [ "$(fld "$1" 1)" != Z ]; }
sig() { local t; for t in $targets; do same "${t%%:*}" "${t#*:}" && kill "-$1" "${t%%:*}" 2>/dev/null; done; }
alive() { local t; for t in $targets; do same "${t%%:*}" "${t#*:}" && return 0; done; return 1; }
[ "$mode" = run ] && { [ -z "$var" ] || [ -z "$val" ]; } && exit 2
[ "$mode" = any ] && [ -z "$var" ] && exit 2
scan
[ -z "$targets" ] && exit 0
sig TERM
i=0; while [ $i -lt 30 ] && alive; do sleep 0.1; i=$((i+1)); done
for r in 1 2 3 4 5; do scan; [ -z "$targets" ] && break; sig STOP; sig KILL; sleep 0.2; done
scan
for t in $targets; do echo "survivor pid=${t%%:*} cmd=$(cmdof "${t%%:*}")"; done
exit 0`
}

// Matching is the environment that makes Script kill what carries name=value.
func Matching(name, value string) map[string]string {
	return map[string]string{envMode: "run", envVar: name, envValue: value}
}

// Carrying is the environment that makes Script kill what carries name, whatever its value: the
// leftovers of every turn or run marked with name, when none of them can still be alive.
func Carrying(name string) map[string]string {
	return map[string]string{envMode: "any", envVar: name}
}

// All is the environment that makes Script kill every process it does not spare. keepGlob names the
// pid files of process groups to spare; empty spares none.
func All(keepGlob string) map[string]string {
	return map[string]string{envMode: "all", envKeep: keepGlob}
}

// WithKeep adds the pid files to spare to a Matching environment.
func WithKeep(env map[string]string, keepGlob string) map[string]string {
	env[envKeep] = keepGlob
	return env
}

// Line is one process Script reported.
type Line struct {
	// Survivor is a process still alive after every kill round.
	Survivor bool
	// Process is "pid=<pid> rss_kb=<kB> cmd=<command line>" (survivors carry no rss).
	Process string
}

func Parse(stdout string) []Line {
	var out []Line
	for line := range strings.Lines(stdout) {
		line = strings.TrimSpace(line)
		if p, ok := strings.CutPrefix(line, "reap "); ok {
			out = append(out, Line{Process: p})
		} else if p, ok := strings.CutPrefix(line, "survivor "); ok {
			out = append(out, Line{Survivor: true, Process: p})
		}
	}
	return out
}
