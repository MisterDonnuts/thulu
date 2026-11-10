#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

# BEGIN i18n: written by `go test ./internal/i18n/shell -update` from
# internal/i18n/shell/locales. Edit the catalog, then regenerate.
#
# msg KEY [NAME VALUE]... prints a message in the reader's language;
# msg_en prints it in English, for logs and for text kept outside Thulu.
# A count chooses between a message's plural forms.
# The language is OCTOPUS_LOCALE, else the terminal's (LC_ALL, LC_MESSAGES, LANG).
_i18n_locale() {
  for _i18n_v in "${OCTOPUS_LOCALE:-}" "${LC_ALL:-}" "${LC_MESSAGES:-}" "${LANG:-}"; do
    case $_i18n_v in
    '') ;;
    es | es[_.@-]*) echo es; return ;;
    *) echo en; return ;;
    esac
  done
  echo en
}
# shellcheck disable=SC2016 # literal text: a $ or ` in a message is for the reader to type
_i18n_tmpl() {
  case $1 in
  'en:common.errors.need_json_tool') _i18n_t='need '\''node'\'' or '\''python3'\'' on PATH to parse JSON.' ;;
  'en:common.errors.unknown_argument') _i18n_t='unknown argument '\''{arg}'\'' (see --help)' ;;
  'en:setup_auth.cli.copy_token') _i18n_t='OAuth token (copy into Settings → AI Engines → Claude Code, pick
    "OAuth token (Claude Pro/Max): installation owner only, for testing"):' ;;
  'en:setup_auth.cli.installed') _i18n_t='Installed BYOK OAuth key on {server} for engine '\''{engine}'\'' in workspace {workspace}.' ;;
  'en:setup_auth.cli.paste_token') _i18n_t='Paste the token it printed (sk-ant-oat…), then Enter: ' ;;
  'en:setup_auth.cli.pushing') _i18n_t='Pushing OAuth token to {server} (engine: {engine}, workspace: {workspace})…' ;;
  'en:setup_auth.cli.running_setup_token') _i18n_t='Running `claude setup-token`: sign in with the Claude account in the browser it opens.' ;;
  'en:setup_auth.cli.usage') _i18n_t='Create a Claude Code OAuth token with `claude setup-token` and write it to <repo>/.env.
The token bills a Claude Pro/Max subscription: Thulu keeps it for the
installation owner'\''s testing, and only the owner can store it on a daemon.

Usage:
  scripts/setup-auth.sh                 # local only: write .env
  scripts/setup-auth.sh --push          # also push to $OCTOPUS_SERVER_URL
                                        # (default http://localhost:8585)
  scripts/setup-auth.sh --push=http://host:8585
  scripts/setup-auth.sh --push --workspace <id>
  printf '\''%s\n'\'' "$TOKEN" | scripts/setup-auth.sh --push   # token from stdin

Push knobs (env — the first two are required for --push):
  OCTOPUS_API_KEY        owner key minted in OCTOPUS_OPERATOR_WORKSPACE (oct_…)
  OCTOPUS_WORKSPACE_ID   workspace to install into (--workspace overrides)
  OCTOPUS_SERVER_URL     remote base URL           (default http://localhost:8585)
  OCTOPUS_LLM_ENGINE     engine name for the key   (default claude-code)

The workspace id is the `workspace_id` of GET /auth/me in a logged-in browser
(DevTools → Network), or the `workspace_id` column of the daemon'\''s `projects`
table.' ;;
  'en:setup_auth.cli.wrote_token') _i18n_t='Wrote token to {file}
    The daemon'\''s claude-code engine reads this file fresh on every
    invocation — no restart needed.' ;;
  'en:setup_auth.errors.no_claude') _i18n_t=''\''claude'\'' not found on PATH. Install Claude Code (npm install -g @anthropic-ai/claude-code)
       or pipe a token you already have into this script.' ;;
  'en:setup_auth.errors.not_oauth_token') _i18n_t='that is not a Claude Code OAuth token (they start with sk-ant-oat).' ;;
  'en:setup_auth.errors.push_needs_curl') _i18n_t='--push needs '\''curl'\'' on PATH.' ;;
  'en:setup_auth.errors.push_needs_key') _i18n_t='--push needs OCTOPUS_API_KEY (an unscoped operator key, oct_…).' ;;
  'en:setup_auth.errors.push_needs_workspace') _i18n_t='--push needs the workspace to install into.
       Pass --workspace <id> or set OCTOPUS_WORKSPACE_ID. Without it the
       key lands in the workspace named "default", which no agent run
       reads — the push would report success and change nothing.' ;;
  'en:setup_auth.errors.push_owner_only') _i18n_t='the daemon refused the token (403): only the installation owner can store it.
       OCTOPUS_API_KEY has to be an owner key minted in the workspace OCTOPUS_OPERATOR_WORKSPACE names.' ;;
  'en:setup_auth.errors.put_failed') _i18n_t='PUT /llm-key failed (key rejected? engine name? server up?).' ;;
  'en:setup_auth.errors.workspace_mismatch') _i18n_t='asked for workspace '\''{asked}'\'' but the key landed in '\''{landed}'\''.
       The server is older than this script (no ?workspace= support).' ;;
  'en:setup_auth.labels.none') _i18n_t='<none>' ;;
  'es:common.errors.need_json_tool') _i18n_t='hace falta '\''node'\'' o '\''python3'\'' en el PATH para leer JSON.' ;;
  'es:common.errors.unknown_argument') _i18n_t='argumento desconocido '\''{arg}'\'' (mirá --help)' ;;
  'es:setup_auth.cli.copy_token') _i18n_t='Token OAuth (copialo en Settings → AI Engines → Claude Code, elegí
    "Token OAuth (Claude Pro/Max): sólo el dueño de la instalación, para pruebas"):' ;;
  'es:setup_auth.cli.installed') _i18n_t='Key OAuth BYOK instalada en {server} para el engine '\''{engine}'\'' en el workspace {workspace}.' ;;
  'es:setup_auth.cli.paste_token') _i18n_t='Pegá el token que imprimió (sk-ant-oat…) y Enter: ' ;;
  'es:setup_auth.cli.pushing') _i18n_t='Subiendo el token OAuth a {server} (engine: {engine}, workspace: {workspace})…' ;;
  'es:setup_auth.cli.running_setup_token') _i18n_t='Corriendo `claude setup-token`: entrá con la cuenta de Claude en el navegador que abre.' ;;
  'es:setup_auth.cli.usage') _i18n_t='Crea un token OAuth de Claude Code con `claude setup-token` y lo escribe en <repo>/.env.
El token factura una suscripción Claude Pro/Max: Thulu lo mantiene para las
pruebas del dueño de la instalación, y sólo el dueño puede guardarlo en un daemon.

Uso:
  scripts/setup-auth.sh                 # sólo local: escribe .env
  scripts/setup-auth.sh --push          # además lo sube a $OCTOPUS_SERVER_URL
                                        # (por defecto http://localhost:8585)
  scripts/setup-auth.sh --push=http://host:8585
  scripts/setup-auth.sh --push --workspace <id>
  printf '\''%s\n'\'' "$TOKEN" | scripts/setup-auth.sh --push   # token por stdin

Variables del push (las dos primeras son obligatorias para --push):
  OCTOPUS_API_KEY        key de owner creada en OCTOPUS_OPERATOR_WORKSPACE (oct_…)
  OCTOPUS_WORKSPACE_ID   workspace donde instalarlo (--workspace la pisa)
  OCTOPUS_SERVER_URL     URL base remota          (por defecto http://localhost:8585)
  OCTOPUS_LLM_ENGINE     engine de la key          (por defecto claude-code)

El id del workspace es el `workspace_id` de GET /auth/me en un navegador logueado
(DevTools → Network), o la columna `workspace_id` de la tabla `projects` del
daemon.' ;;
  'es:setup_auth.cli.wrote_token') _i18n_t='Token escrito en {file}
    El engine claude-code del daemon relee este archivo en cada
    invocación — no hace falta reiniciar.' ;;
  'es:setup_auth.errors.no_claude') _i18n_t='no encontré '\''claude'\'' en el PATH. Instalá Claude Code (npm install -g @anthropic-ai/claude-code)
       o pasale por stdin un token que ya tengas.' ;;
  'es:setup_auth.errors.not_oauth_token') _i18n_t='eso no es un token OAuth de Claude Code (empiezan con sk-ant-oat).' ;;
  'es:setup_auth.errors.push_needs_curl') _i18n_t='--push necesita '\''curl'\'' en el PATH.' ;;
  'es:setup_auth.errors.push_needs_key') _i18n_t='--push necesita OCTOPUS_API_KEY (una key de operador sin scope, oct_…).' ;;
  'es:setup_auth.errors.push_needs_workspace') _i18n_t='--push necesita el workspace donde instalarla.
       Pasá --workspace <id> o seteá OCTOPUS_WORKSPACE_ID. Sin eso la
       key cae en el workspace llamado "default", que ninguna corrida de
       agente lee — el push diría que funcionó y no cambiaría nada.' ;;
  'es:setup_auth.errors.push_owner_only') _i18n_t='el daemon rechazó el token (403): sólo el dueño de la instalación puede guardarlo.
       OCTOPUS_API_KEY tiene que ser una key de owner creada en el workspace que nombra OCTOPUS_OPERATOR_WORKSPACE.' ;;
  'es:setup_auth.errors.put_failed') _i18n_t='falló PUT /llm-key (¿key rechazada? ¿nombre del engine? ¿servidor caído?).' ;;
  'es:setup_auth.errors.workspace_mismatch') _i18n_t='se pidió el workspace '\''{asked}'\'' pero la key cayó en '\''{landed}'\''.
       El servidor es más viejo que este script (no soporta ?workspace=).' ;;
  'es:setup_auth.labels.none') _i18n_t='<ninguno>' ;;
  *) return 1 ;;
  esac
}
_i18n_render() {
  _i18n_loc=$1 _i18n_key=$2 _i18n_n='' _i18n_name=''
  shift 2
  for _i18n_a in "$@"; do
    if [ -z "$_i18n_name" ]; then _i18n_name=$_i18n_a; continue; fi
    if [ "$_i18n_name" = count ]; then _i18n_n=$_i18n_a; fi
    _i18n_name=
  done
  _i18n_form=:other
  if [ "$_i18n_n" = 1 ]; then _i18n_form=:one; fi
  _i18n_tmpl "$_i18n_loc:$_i18n_key$_i18n_form" || _i18n_tmpl "$_i18n_loc:$_i18n_key" ||
    _i18n_tmpl "en:$_i18n_key$_i18n_form" || _i18n_tmpl "en:$_i18n_key" || _i18n_t=$_i18n_key
  _i18n_out=
  while :; do
    case $_i18n_t in *'{'*) ;; *) break ;; esac
    _i18n_out=$_i18n_out${_i18n_t%%'{'*}
    _i18n_t=${_i18n_t#*'{'}
    _i18n_ph=${_i18n_t%%'}'*} _i18n_hit=
    case $_i18n_t in
    *'}'*)
      for _i18n_a in "$@"; do
        if [ -z "$_i18n_name" ]; then _i18n_name=$_i18n_a; continue; fi
        if [ "$_i18n_name" = "$_i18n_ph" ]; then _i18n_hit=1 _i18n_val=$_i18n_a; fi
        _i18n_name=
      done
      ;;
    esac
    if [ -n "$_i18n_hit" ]; then
      _i18n_out=$_i18n_out$_i18n_val
      _i18n_t=${_i18n_t#*'}'}
    else
      _i18n_out=$_i18n_out'{'
    fi
  done
  printf '%s\n' "$_i18n_out$_i18n_t"
}
msg() { _i18n_render "$(_i18n_locale)" "$@"; }
msg_en() { _i18n_render en "$@"; }
# END i18n

PUSH=0
SERVER_URL="${OCTOPUS_SERVER_URL:-http://localhost:8585}"
WORKSPACE_ID="${OCTOPUS_WORKSPACE_ID:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --push)        PUSH=1; shift ;;
    --push=*)      PUSH=1; SERVER_URL="${1#--push=}"; shift ;;
    --workspace)   WORKSPACE_ID="${2:-}"; shift 2 ;;
    --workspace=*) WORKSPACE_ID="${1#--workspace=}"; shift ;;
    -h|--help)     msg setup_auth.cli.usage; exit 0 ;;
    *) echo "ERROR: $(msg common.errors.unknown_argument arg "$1")" >&2; exit 2 ;;
  esac
done

if command -v node >/dev/null 2>&1; then
  JSON_TOOL=node
elif command -v python3 >/dev/null 2>&1; then
  JSON_TOOL=python3
else
  echo "ERROR: $(msg common.errors.need_json_tool)" >&2
  exit 1
fi

json_get() {
  if [ "$JSON_TOOL" = node ]; then
    # i18n-ignore JavaScript, not a message
    node -e 'let b="";process.stdin.on("data",d=>b+=d);process.stdin.on("end",()=>{let o=JSON.parse(b);for(const k of process.argv[1].split("."))o=(o==null?o:o[k]);console.log(o==null?"":o);});' "$1"
  else
    # i18n-ignore Python, not a message
    python3 -c 'import json,sys
o=json.load(sys.stdin)
for k in sys.argv[1].split("."):
    o=o.get(k) if isinstance(o,dict) else None
print("" if o is None else o)' "$1"
  fi
}

# setup-token draws its own interactive screen, so its output cannot be captured; the user pastes
# the token.
ACCESS_TOKEN=""
if [ -t 0 ]; then
  if ! command -v claude >/dev/null 2>&1; then
    echo "ERROR: $(msg setup_auth.errors.no_claude)" >&2
    exit 1
  fi
  echo "==> $(msg setup_auth.cli.running_setup_token)"
  claude setup-token
  echo
  printf '%s' "$(msg setup_auth.cli.paste_token)"
  IFS= read -rs ACCESS_TOKEN
  echo
else
  IFS= read -r ACCESS_TOKEN || true
fi
ACCESS_TOKEN="$(printf '%s' "$ACCESS_TOKEN" | tr -d '[:space:]')"

case "$ACCESS_TOKEN" in
  sk-ant-oat*) ;;
  *) echo "ERROR: $(msg setup_auth.errors.not_oauth_token)" >&2; exit 1 ;;
esac

TMP_FILE=$(mktemp)
trap 'rm -f "$TMP_FILE"' EXIT
if [ -f "$ENV_FILE" ]; then
  grep -v '^CLAUDE_CODE_OAUTH_TOKEN=' "$ENV_FILE" > "$TMP_FILE" || true
fi
printf 'CLAUDE_CODE_OAUTH_TOKEN=%s\n' "$ACCESS_TOKEN" >> "$TMP_FILE"

mv "$TMP_FILE" "$ENV_FILE"
chmod 600 "$ENV_FILE"
trap - EXIT

echo "==> $(msg setup_auth.cli.wrote_token file "$ENV_FILE")"

if [ "$PUSH" = 1 ]; then
  if ! command -v curl >/dev/null 2>&1; then
    echo "ERROR: $(msg setup_auth.errors.push_needs_curl)" >&2
    exit 1
  fi

  ENGINE="${OCTOPUS_LLM_ENGINE:-claude-code}"
  API_KEY="${OCTOPUS_API_KEY:-}"

  if [ -z "$API_KEY" ]; then
    echo "ERROR: $(msg setup_auth.errors.push_needs_key)" >&2
    exit 1
  fi
  if [ -z "$WORKSPACE_ID" ]; then
    echo "ERROR: $(msg setup_auth.errors.push_needs_workspace)" >&2
    exit 1
  fi

  echo
  echo "==> $(msg setup_auth.cli.pushing server "$SERVER_URL" engine "$ENGINE" workspace "$WORKSPACE_ID")"

  # API key goes in a curl config file, not -H: argv is readable through `ps` and operator keys are
  # long-lived.
  CURL_CFG="$(mktemp)"
  trap 'rm -f "$CURL_CFG"' EXIT
  printf 'header = "Authorization: Bearer %s"\n' "$API_KEY" > "$CURL_CFG" # i18n-ignore a curl config line

  # Token goes via stdin so it never appears in argv.
  PUT_BODY="$(mktemp)"
  trap 'rm -f "$CURL_CFG" "$PUT_BODY"' EXIT
  PUT_STATUS=$(
    # i18n-ignore a JSON body, not a message
    printf '{"engine":"%s","kind":"oauth","key":"%s"}' "$ENGINE" "$ACCESS_TOKEN" \
      | curl -sS -o "$PUT_BODY" -w '%{http_code}' -X PUT "$SERVER_URL/llm-key?workspace=$WORKSPACE_ID" \
          -K "$CURL_CFG" -H 'Content-Type: application/json' -d @-
  ) || { echo "ERROR: $(msg setup_auth.errors.put_failed)" >&2; exit 1; }
  PUT_RESP="$(cat "$PUT_BODY")"
  rm -f "$CURL_CFG" "$PUT_BODY"
  trap - EXIT

  case "$PUT_STATUS" in
    2??) ;;
    403) echo "ERROR: $(msg setup_auth.errors.push_owner_only)" >&2; exit 1 ;;
    *) echo "ERROR: $(msg setup_auth.errors.put_failed)" >&2; printf '%s\n' "$PUT_RESP" >&2; exit 1 ;;
  esac

  # A key without ?workspace= lands in workspace "default", which no run reads, and still answers
  # 200.
  # Only the response's workspace_id shows where the key landed.
  LANDED=$(printf '%s' "$PUT_RESP" | json_get workspace_id)
  if [ "$LANDED" != "$WORKSPACE_ID" ]; then
    echo "ERROR: $(msg setup_auth.errors.workspace_mismatch asked "$WORKSPACE_ID" \
      landed "${LANDED:-$(msg setup_auth.labels.none)}")" >&2
    exit 1
  fi
  echo "==> $(msg setup_auth.cli.installed server "$SERVER_URL" engine "$ENGINE" workspace "$LANDED")"
fi

echo
echo "==> $(msg setup_auth.cli.copy_token)"
echo
printf '%s\n' "$ACCESS_TOKEN"
echo
