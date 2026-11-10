#!/bin/sh
# Substitutes only the variables listed below, with defaults, so other `$` in templates survive.
# Plain envsubst blanks unset ports and CIDRs and renders broken policies.
set -eu

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
_i18n_tmpl() {
  case $1 in
  'en:render.errors.tenant_required') _i18n_t='TENANT_ID is required for {template}' ;;
  'en:render.errors.usage') _i18n_t='usage: render.sh <template.yaml>' ;;
  'es:render.errors.tenant_required') _i18n_t='TENANT_ID es obligatorio para {template}' ;;
  'es:render.errors.usage') _i18n_t='uso: render.sh <template.yaml>' ;;
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

if [ -z "${1:-}" ]; then
  msg render.errors.usage >&2
  exit 2
fi
template="$1"
case "$template" in
  */*) ;;
  *) template="$(dirname "$0")/$template" ;;
esac

# shellcheck disable=SC2016 # a literal ${TENANT_ID}, searched for in the template
if grep -q '${TENANT_ID}' "$template"; then
  if [ -z "${TENANT_ID:-}" ]; then
    msg render.errors.tenant_required template "$template" >&2
    exit 1
  fi
fi
export TENANT_ID="${TENANT_ID:-}"
export CONCURRENCY_LIMIT="${CONCURRENCY_LIMIT:-5}"
export INTERNAL_CIDRS="${INTERNAL_CIDRS:-10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16}"
export CONTROL_PLANE_LABEL="${CONTROL_PLANE_LABEL:-control}"
export DAEMON_PORT="${DAEMON_PORT:-8585}"
export GATEWAY_PORT="${GATEWAY_PORT:-8586}"
export MCP_PORT="${MCP_PORT:-3000}"
export EXEC_PORT="${EXEC_PORT:-8587}"
export DAEMON_NAME="${DAEMON_NAME:-octopus}"
export IMAGE_REGISTRY="${IMAGE_REGISTRY:-ghcr.io/misterdonnuts}"
export SIGNING_SUBJECT="${SIGNING_SUBJECT:-https://github.com/MisterDonnuts/thulu/.github/workflows/octopus-release.yml@refs/tags/v*}"
export REKOR_URL="${REKOR_URL:-https://rekor.sigstore.dev}"

# shellcheck disable=SC2016 # the list names variables for envsubst, not the shell
envsubst '${TENANT_ID} ${CONCURRENCY_LIMIT} ${INTERNAL_CIDRS} ${CONTROL_PLANE_LABEL} ${DAEMON_PORT} ${GATEWAY_PORT} ${MCP_PORT} ${EXEC_PORT} ${DAEMON_NAME} ${IMAGE_REGISTRY} ${SIGNING_SUBJECT} ${REKOR_URL}' <"$template"
