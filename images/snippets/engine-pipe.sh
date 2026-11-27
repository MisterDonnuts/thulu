#!/usr/bin/env bash
# Sourced by entrypoint-base.sh. Runs the engine with its output copied to the log and the terminal
# (docker logs, kubectl logs) without letting what it leaves behind keep the run alive.
#
# A plain `engine | tee -a session.log` waits for every process that holds the pipe: a dev server
# the agent started in the background with its stdout unredirected kept tee, and so the entrypoint,
# the container and the pod, running until the run deadline.

_THULU_ENGINE_PGID=""
_THULU_ENGINE_SIGNAL=""

_thulu_engine_forward() {
  _THULU_ENGINE_SIGNAL="$1"
  [ -n "$_THULU_ENGINE_PGID" ] && kill "-$1" -- "-$_THULU_ENGINE_PGID" 2>/dev/null
  return 0
}

# _thulu_kill_group PGID: TERM, up to three seconds of grace, then STOP+KILL rounds until no process
# of the group is left.
_thulu_kill_group() {
  local pg="$1" i=0
  kill -0 -- "-$pg" 2>/dev/null || return 0
  kill -TERM -- "-$pg" 2>/dev/null
  while [ "$i" -lt 30 ] && kill -0 -- "-$pg" 2>/dev/null; do
    sleep 0.1
    i=$((i + 1))
  done
  for i in 1 2 3 4 5; do
    kill -0 -- "-$pg" 2>/dev/null || return 0
    kill -STOP -- "-$pg" 2>/dev/null
    kill -KILL -- "-$pg" 2>/dev/null
    sleep 0.1
  done
  return 0
}

# _thulu_engine_pipe LOG CMD...: runs CMD in its own process group with stdout and stderr appended to
# LOG and copied to stdout, and returns CMD's exit code. When CMD exits, what it left in its group is
# killed, and the copy gets five seconds to drain before it stops, even if a process that left the
# group (setsid) still holds the pipe. TERM, INT and HUP reach CMD's group; once it is gone, this
# shell takes the same signal, as it did before.
_thulu_engine_pipe() {
  local log="$1" dir fifo tee_pid code i sig
  shift
  dir=$(mktemp -d "${TMPDIR:-/tmp}/thulu-engine.XXXXXX") || dir=""
  if [ -z "$dir" ] || ! mkfifo "$dir/out"; then
    [ -n "$dir" ] && rm -rf "$dir"
    "$@" 2>&1 | tee -a "$log"
    return "${PIPESTATUS[0]}"
  fi
  fifo="$dir/out"
  tee -a "$log" <"$fifo" &
  tee_pid=$!
  _THULU_ENGINE_SIGNAL=""
  trap '_thulu_engine_forward TERM' TERM # i18n-ignore a trap handler, not a message
  trap '_thulu_engine_forward INT' INT # i18n-ignore a trap handler, not a message
  trap '_thulu_engine_forward HUP' HUP # i18n-ignore a trap handler, not a message
  # Job control gives the background job a process group of its own, led by its pid.
  set -m
  "$@" >"$fifo" 2>&1 &
  _THULU_ENGINE_PGID=$!
  set +m
  while :; do
    wait "$_THULU_ENGINE_PGID"
    code=$?
    # Above 128 is a trapped signal cutting the wait short, or a death by signal; only a live (or
    # not yet reaped) child is waited for again.
    [ "$code" -le 128 ] && break
    kill -0 "$_THULU_ENGINE_PGID" 2>/dev/null || break
  done
  _thulu_kill_group "$_THULU_ENGINE_PGID"
  i=0
  while [ "$i" -lt 50 ] && kill -0 "$tee_pid" 2>/dev/null; do
    sleep 0.1
    i=$((i + 1))
  done
  kill "$tee_pid" 2>/dev/null
  wait "$tee_pid" 2>/dev/null
  rm -rf "$dir"
  _THULU_ENGINE_PGID=""
  trap - TERM INT HUP
  sig="$_THULU_ENGINE_SIGNAL"
  if [ -n "$sig" ]; then
    kill "-$sig" "$$"
  fi
  return "$code"
}
