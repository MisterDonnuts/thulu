package sandbox

// octopus_setup_key is the shell twin of project/setupkey.go; TestSetupKeyGoMatchesShell keeps them
// in sync.
// setup.result's key=value lines are read back by parseInitProbe.

import (
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
)

const (
	SetupStateNone     = "none"
	SetupStateRunning  = "running"
	SetupStateSkipped  = "skipped"
	SetupStateRestored = "restored"
	SetupStateRan      = "ran"
	SetupStateFailed   = "failed"
)

const initSetupMaxSeconds = 1800

type InitSetupReport struct {
	Status          string   `json:"status"`
	Key             string   `json:"key,omitempty"`
	Snapshot        string   `json:"snapshot,omitempty"`
	Tools           string   `json:"tools,omitempty"`
	ToolsDownloaded []string `json:"tools_downloaded,omitempty"`
	ToolsUsed       []string `json:"-"`
	RunID           string   `json:"-"`
	ToolsMs         int64    `json:"tools_ms,omitempty"`
	RestoreMs       int64    `json:"restore_ms,omitempty"`
	SetupMs         int64    `json:"setup_ms,omitempty"`
	ExitCode        *int     `json:"exit_code,omitempty"`
	NoCache         bool     `json:"no_cache,omitempty"`
}

func (r *InitSetupReport) parseSetupLine(k, v string) bool {
	if k == "setup_run" {
		r.RunID = v
		return false
	}
	n, nErr := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	switch k {
	case "setup_status":
		r.Status = v
	case "setup_key":
		r.Key = v
	case "setup_snapshot":
		r.Snapshot = v
	case "setup_tools":
		if v != "none" {
			r.Tools = v
		}
	case "setup_note":
		r.NoCache = v == "no_cache"
	case "tools_downloaded":
		r.ToolsDownloaded = splitToolList(v)
	case "tools_used":
		r.ToolsUsed = splitToolList(v)
	case "tools_ms":
		if nErr == nil {
			r.ToolsMs = n
		}
	case "restore_ms":
		if nErr == nil {
			r.RestoreMs = n
		}
	case "setup_ms":
		if nErr == nil {
			r.SetupMs = n
		}
	case "setup_exit":
		if nErr == nil {
			c := int(n)
			r.ExitCode = &c
		}
	default:
		return false
	}
	return true
}

func splitToolList(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

type setupSpec struct {
	dir       string
	repo      string
	projectID string
	manifest  project.InitManifest
}

func setupToolArgs(tools map[string]string) []string {
	line := project.SetupToolsLine(tools)
	if line == "" {
		return nil
	}
	pairs := strings.Split(line, ",")
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		name, version, _ := strings.Cut(p, "=")
		out = append(out, name+"@"+version)
	}
	return out
}

func shellArray(items []string) string {
	q := make([]string, 0, len(items))
	for _, it := range items {
		q = append(q, shellSingleQuote(it))
	}
	return "(" + strings.Join(q, " ") + ")"
}

func setupPrepareScript(spec setupSpec) string {
	m := spec.manifest
	has := "0"
	if m.Setup != "" {
		has = "1"
	}
	var b strings.Builder
	set := func(name, value string) { b.WriteString(name + "=" + value + "\n") }
	set("d", shellSingleQuote(spec.dir))
	set("setup_repo", shellSingleQuote(spec.repo))
	set("setup_project", shellSingleQuote(spec.projectID))
	set("setup_has", has)
	set("setup_tools_args", shellArray(setupToolArgs(m.Tools)))
	set("setup_tools_line", shellSingleQuote(project.SetupToolsLine(m.Tools)))
	set("setup_files_digest", shellSingleQuote(project.SetupFilesDigest(m.Files)))
	set("setup_keys", shellArray(m.Cache.Key))
	set("setup_implicit", shellArray(project.SetupImplicitFiles(m)))
	set("setup_paths", shellArray(m.Cache.Paths))
	for _, msg := range []struct{ name, key string }{
		{"msg_tools", "sandbox.init_setup.tools_installing"},
		{"msg_no_mise", "sandbox.init_setup.no_mise"},
		{"msg_no_cache", "sandbox.init_setup.no_cache"},
		{"msg_skipped", "sandbox.init_setup.skipped"},
		{"msg_restoring", "sandbox.init_setup.restoring"},
		{"msg_restore_failed", "sandbox.init_setup.restore_failed"},
		{"msg_setup_running", "sandbox.init_setup.running"},
		{"msg_setup_failed", "sandbox.init_setup.failed"},
		{"msg_start", "sandbox.init_setup.start"},
	} {
		set(msg.name, shellSingleQuote(i18n.Text(msg.key)))
	}
	b.WriteString(initSetupLib)
	return b.String()
}

// Don't add `set -u`: empty arrays here must expand to nothing.
//
//i18n:ignore shell library run in the sandbox
const initSetupLib = `
OCTOPUS_MX=()
setup_tools_state=none
tools_ms=0

tools_downloaded=
tools_used=

octopus_now_ms() {
  local t; t=$(date +%s%3N 2>/dev/null)
  case "$t" in ''|*N*) echo $(( $(date +%s) * 1000 ));; *) echo "$t";; esac
}
octopus_sha() { sha256sum | cut -d' ' -f1; }
octopus_report() { printf '%s=%s\n' "$1" "$2" >> "$d/setup.result"; }
octopus_phase() { echo "$1" > "$d/phase"; }

# The setup key, see project/setupkey.go for the canonical text.
octopus_setup_key() {
  {
    printf '%s\n' 'octopus-setup-v1'
    printf 'setup:%s\n' "$(octopus_sha < "$d/setup.sh")"
    printf 'tools:%s\n' "$setup_tools_line"
    if [ -n "$setup_files_digest" ]; then printf 'files:%s\n' "$setup_files_digest"; fi
    local f
    for f in "${setup_keys[@]}"; do
      if [ -f "$f" ]; then printf 'file:%s:%s\n' "$f" "$(octopus_sha < "$f")"; else printf 'file:%s:-\n' "$f"; fi
    done
    for f in "${setup_implicit[@]}"; do
      if [ -f "$f" ]; then printf 'file:%s:%s\n' "$f" "$(octopus_sha < "$f")"; fi
    done
  } | octopus_sha
}

octopus_has_mise_file() {
  local f
  for f in .tool-versions mise.toml .mise.toml; do [ -f "$f" ] && return 0; done
  return 1
}

# The tool@version directories mise has installed, one per line. Version
# aliases (latest, 22) are symlinks and are left out.
octopus_mise_installs() {
  local root t v
  root="${MISE_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/mise}/installs"
  [ -d "$root" ] || return 0
  for t in "$root"/*/; do
    t=${t%/}
    for v in "$t"/*; do
      { [ -d "$v" ] && [ ! -L "$v" ]; } || continue
      printf '%s@%s\n' "${t##*/}" "${v##*/}"
    done
  done
}
# Sets tools_downloaded (what the install added to the list taken before it)
# and tools_used (what the run asked for), both comma-separated.
octopus_tools_seen() {
  local before="$1" x used
  tools_downloaded=$(octopus_mise_installs | while IFS= read -r x; do
    printf '%s\n' "$before" | grep -Fxq -- "$x" || printf '%s,' "$x"
  done)
  tools_downloaded=${tools_downloaded%,}
  used=$(printf '%s,' "${setup_tools_args[@]}")
  if octopus_has_mise_file; then
    used="$used$(mise current 2>/dev/null | awk '{for (i = 2; i <= NF; i++) printf "%s@%s,", $1, $i}')"
  fi
  tools_used=${used%,}
}

# Installs the toolchains and points OCTOPUS_MX at mise exec. Without mise in
# the image it says so and carries on: the script may not need them.
octopus_tools() {
  local t0 before
  if [ ${#setup_tools_args[@]} -eq 0 ] && ! octopus_has_mise_file; then return 0; fi
  if ! command -v mise >/dev/null 2>&1; then
    setup_tools_state=unavailable
    echo "[octopus] $msg_no_mise"
    return 0
  fi
  export MISE_YES=1 MISE_TRUSTED_CONFIG_PATHS="$setup_repo"
  t0=$(octopus_now_ms)
  before=$(octopus_mise_installs)
  echo "[octopus] $msg_tools ${setup_tools_args[*]}"
  if octopus_has_mise_file && ! mise install; then
    setup_tools_state=failed; tools_ms=$(( $(octopus_now_ms) - t0 )); octopus_tools_seen "$before"; return 1
  fi
  if [ ${#setup_tools_args[@]} -gt 0 ] && ! mise install "${setup_tools_args[@]}"; then
    setup_tools_state=failed; tools_ms=$(( $(octopus_now_ms) - t0 )); octopus_tools_seen "$before"; return 1
  fi
  tools_ms=$(( $(octopus_now_ms) - t0 ))
  octopus_tools_seen "$before"
  setup_tools_state=installed
  OCTOPUS_MX=(mise exec "${setup_tools_args[@]}" --)
  return 0
}

# Markers: the checkout's git dir when writable, and the run's directory.
octopus_marker_files() {
  local gd
  gd=$(git rev-parse --absolute-git-dir 2>/dev/null)
  if [ -n "$gd" ] && [ -d "$gd" ] && [ -w "$gd" ]; then printf '%s\n' "$gd/octopus-setup.marker"; fi
  printf '%s\n' "$d/setup.marker"
}
# First line the key, then the cache paths that existed after the setup.
octopus_marker_ok() {
  local m k p
  while IFS= read -r m; do
    [ -f "$m" ] || continue
    k=$(head -n 1 "$m")
    [ "$k" = "$1" ] || return 1
    while IFS= read -r p; do
      [ -z "$p" ] || [ -e "$p" ] || return 1
    done < <(tail -n +2 "$m")
    return 0
  done < <(octopus_marker_files)
  return 1
}
octopus_marker_write() {
  local m p
  while IFS= read -r m; do
    {
      printf '%s\n' "$1"
      for p in "${setup_paths[@]}"; do [ -e "$p" ] && printf '%s\n' "$p"; done
    } > "$m" 2>/dev/null
  done < <(octopus_marker_files)
}
octopus_marker_clear() {
  local m
  while IFS= read -r m; do rm -f "$m"; done < <(octopus_marker_files)
}

octopus_run_setup() {
  local t0 code
  t0=$(octopus_now_ms)
  echo "[octopus] $msg_setup_running"
  octopus_marker_clear
  "${OCTOPUS_MX[@]}" bash "$d/setup.sh"
  code=$?
  octopus_report setup_ms $(( $(octopus_now_ms) - t0 ))
  if [ "$code" -ne 0 ]; then
    octopus_report setup_status failed
    octopus_report setup_exit "$code"
    echo "[octopus] $msg_setup_failed $code"
    return "$code"
  fi
  octopus_report setup_status ran
  if [ -n "$OCTOPUS_CACHE_DIR" ]; then octopus_marker_write "$1"; fi
  return 0
}

# A task's run: tools, then skip, restore or run the setup.
octopus_prepare() {
  local key snap t0 p
  : > "$d/setup.result"
  octopus_report setup_run "$$-$(octopus_now_ms)"
  octopus_phase tools
  if ! octopus_tools; then
    octopus_report setup_tools failed
    octopus_report tools_ms "$tools_ms"
    octopus_report tools_downloaded "$tools_downloaded"
    octopus_report tools_used "$tools_used"
    octopus_report setup_status failed
    return 1
  fi
  octopus_report setup_tools "$setup_tools_state"
  octopus_report tools_ms "$tools_ms"
  octopus_report tools_downloaded "$tools_downloaded"
  octopus_report tools_used "$tools_used"
  if [ "$setup_has" != 1 ]; then
    octopus_report setup_status none
    octopus_phase start
    return 0
  fi
  octopus_phase setup
  key=$(octopus_setup_key)
  octopus_report setup_key "$key"
  if [ -z "$OCTOPUS_CACHE_DIR" ]; then
    echo "[octopus] $msg_no_cache"
    octopus_report setup_note no_cache
    octopus_run_setup "$key" || return
    echo "[octopus] $msg_start"
    octopus_phase start
    return 0
  fi
  if octopus_marker_ok "$key"; then
    echo "[octopus] $msg_skipped $key"
    octopus_report setup_status skipped
    echo "[octopus] $msg_start"
    octopus_phase start
    return 0
  fi
  snap="$OCTOPUS_CACHE_DIR/snapshots/$setup_project/$key.tar.zst"
  if [ -f "$snap" ] && [ ${#setup_paths[@]} -gt 0 ] && command -v zstd >/dev/null 2>&1; then
    t0=$(octopus_now_ms)
    echo "[octopus] $msg_restoring $key"
    octopus_marker_clear
    for p in "${setup_paths[@]}"; do rm -rf -- "$p"; done
    if tar --zstd -xf "$snap" -C .; then
      octopus_report restore_ms $(( $(octopus_now_ms) - t0 ))
      octopus_report setup_status restored
      octopus_report setup_snapshot "$key"
      octopus_marker_write "$key"
      echo "[octopus] $msg_start"
      octopus_phase start
      return 0
    fi
    echo "[octopus] $msg_restore_failed"
  fi
  octopus_run_setup "$key" || return
  echo "[octopus] $msg_start"
  octopus_phase start
  return 0
}

# A prewarm: tools and setup of a fresh checkout, then the snapshot. Prints
# prewarm_* lines for the daemon; the exit code says what failed.
octopus_prewarm_tools_out() {
  echo "prewarm_tools_ms=$tools_ms"
  echo "prewarm_tools_downloaded=$tools_downloaded"
  echo "prewarm_tools_used=$tools_used"
}
octopus_prewarm() {
  local dir key snap tmp p old present t0 code
  [ -n "$OCTOPUS_CACHE_DIR" ] || return 10
  dir="$OCTOPUS_CACHE_DIR/snapshots/$setup_project"
  mkdir -p "$dir" 2>/dev/null
  [ -d "$dir" ] && [ -w "$dir" ] || return 11
  command -v zstd >/dev/null 2>&1 || return 12
  if ! octopus_tools >> "$d/prewarm.log" 2>&1; then octopus_prewarm_tools_out; return 13; fi
  octopus_prewarm_tools_out
  [ "$setup_tools_state" = unavailable ] && echo "prewarm_tools=unavailable"
  key=$(octopus_setup_key)
  echo "prewarm_key=$key"
  snap="$dir/$key.tar.zst"
  if [ -f "$snap" ]; then
    echo "prewarm_reused=1"
    touch "$snap"
  else
    t0=$(octopus_now_ms)
    "${OCTOPUS_MX[@]}" bash "$d/setup.sh" >> "$d/prewarm.log" 2>&1
    code=$?
    echo "prewarm_setup_ms=$(( $(octopus_now_ms) - t0 ))"
    [ "$code" -eq 0 ] || return 14
    present=()
    for p in "${setup_paths[@]}"; do [ -e "$p" ] && present+=("$p"); done
    [ ${#present[@]} -gt 0 ] || return 15
    tmp="$dir/.$key.tmp.$$"
    if ! tar --zstd -cf "$tmp" -- "${present[@]}" >> "$d/prewarm.log" 2>&1; then rm -f "$tmp"; return 16; fi
    if ! mv -f "$tmp" "$snap"; then rm -f "$tmp"; return 16; fi
  fi
  echo "prewarm_size=$(wc -c < "$snap" | tr -d ' ')"
  ls -1t "$dir"/*.tar.zst 2>/dev/null | tail -n +3 | while IFS= read -r old; do
    rm -f "$old" && echo "prewarm_pruned=$(basename "$old" .tar.zst)"
  done
  find "$dir" -maxdepth 1 -name '.*.tmp.*' -mmin +120 -delete 2>/dev/null
  echo "prewarm_status=ok"
  return 0
}
`
