#!/usr/bin/env bash

set -euo pipefail
# The dump holds password hashes, API key hashes and encrypted credentials.
umask 077

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
  'en:backup.cli.backing_up') _i18n_t='Backing up Thulu...' ;;
  'en:backup.cli.backup_complete') _i18n_t='Backup complete: {archive} ({size})' ;;
  'en:backup.cli.confirm') _i18n_t='    Continue? [y/N] ' ;;
  'en:backup.cli.copying_data') _i18n_t='   copying the data directory from {from} (without logs/ and runs/)...' ;;
  'en:backup.cli.dumping_db') _i18n_t='   dumping the Postgres database...' ;;
  'en:backup.cli.encrypting') _i18n_t='   encrypting the offsite copy for {recipient}...' ;;
  'en:backup.cli.pruned:one') _i18n_t='   pruned {count} backup beyond the newest {keep}.' ;;
  'en:backup.cli.pruned:other') _i18n_t='   pruned {count} backups beyond the newest {keep}.' ;;
  'en:backup.cli.pushing') _i18n_t='   copying to {remote}...' ;;
  'en:backup.cli.remote_pruned:one') _i18n_t='   pruned {count} offsite backup beyond the newest {keep}.' ;;
  'en:backup.cli.remote_pruned:other') _i18n_t='   pruned {count} offsite backups beyond the newest {keep}.' ;;
  'en:backup.cli.restore_complete') _i18n_t='Restore complete.' ;;
  'en:backup.cli.restore_warning') _i18n_t='    WARNING: This will replace every Thulu table in the target database
    ({database}) and overwrite the data directory ({dir}).' ;;
  'en:backup.cli.restoring') _i18n_t='Restoring from {archive}...' ;;
  'en:backup.cli.restoring_data') _i18n_t='   restoring data directory...' ;;
  'en:backup.cli.restoring_db') _i18n_t='   restoring Postgres database...' ;;
  'en:backup.cli.secretbox_reminder') _i18n_t='   The archive does not hold OCTOPUS_SECRETBOX_KEY. Keep that key apart from it: without the key the credentials in the dump cannot be read back.' ;;
  'en:backup.errors.compose_file_missing') _i18n_t='The compose file '\''{file}'\'' does not exist.' ;;
  'en:backup.errors.data_dir_missing') _i18n_t='The data directory {dir} does not exist. Nothing was backed up: point OCTOPUS_DATA_DIR at the directory that holds projects/ and task-attachments/.' ;;
  'en:backup.errors.data_dir_unreadable') _i18n_t='The data directory {dir} cannot be read by this user. Nothing was backed up; run it as the directory'\''s owner, or as root.' ;;
  'en:backup.errors.database_url_required') _i18n_t='OCTOPUS_DATABASE_URL is required (postgres:// DSN).' ;;
  'en:backup.errors.empty_dump') _i18n_t='pg_dump wrote nothing, so no archive was made.' ;;
  'en:backup.errors.no_data_source') _i18n_t='No data directory to back up. Set OCTOPUS_DATA_DIR (a directory on this machine), OCTOPUS_COMPOSE_FILE (the compose stack'\''s file) or OCTOPUS_BACKUP_SOURCE=kubernetes.' ;;
  'en:backup.errors.pvc_path') _i18n_t='Could not find where PVC {pvc} (namespace {namespace}) keeps its files. Run this on the k3s node, or set OCTOPUS_DATA_DIR to the volume'\''s directory.' ;;
  'en:backup.errors.remote_failed') _i18n_t='Could not copy the backup to {remote}. The local archive {archive} is complete.' ;;
  'en:backup.errors.restore_usage') _i18n_t='Usage: RESTORE=1 ./backup.sh <archive.tar.gz>' ;;
  'en:backup.errors.status_not_written') _i18n_t='Could not write {file}; Settings → Updates will not show this backup. The archive is complete.' ;;
  'en:backup.errors.unknown_source') _i18n_t='OCTOPUS_BACKUP_SOURCE must be host, compose or kubernetes, got '\''{source}'\''.' ;;
  'en:common.errors.not_on_path') _i18n_t=''\''{tool}'\'' is not on PATH.' ;;
  'es:backup.cli.backing_up') _i18n_t='Haciendo backup de Thulu...' ;;
  'es:backup.cli.backup_complete') _i18n_t='Backup completo: {archive} ({size})' ;;
  'es:backup.cli.confirm') _i18n_t='    ¿Seguir? [s/N] ' ;;
  'es:backup.cli.copying_data') _i18n_t='   copiando el directorio de datos desde {from} (sin logs/ ni runs/)...' ;;
  'es:backup.cli.dumping_db') _i18n_t='   volcando la base de Postgres...' ;;
  'es:backup.cli.encrypting') _i18n_t='   cifrando la copia externa para {recipient}...' ;;
  'es:backup.cli.pruned:one') _i18n_t='   se borró {count} backup además de los {keep} más nuevos.' ;;
  'es:backup.cli.pruned:other') _i18n_t='   se borraron {count} backups además de los {keep} más nuevos.' ;;
  'es:backup.cli.pushing') _i18n_t='   copiando a {remote}...' ;;
  'es:backup.cli.remote_pruned:one') _i18n_t='   se borró {count} backup externo además de los {keep} más nuevos.' ;;
  'es:backup.cli.remote_pruned:other') _i18n_t='   se borraron {count} backups externos además de los {keep} más nuevos.' ;;
  'es:backup.cli.restore_complete') _i18n_t='Restauración completa.' ;;
  'es:backup.cli.restore_warning') _i18n_t='    ATENCIÓN: esto reemplaza todas las tablas de Thulu en la base de destino
    ({database}) y pisa el directorio de datos ({dir}).' ;;
  'es:backup.cli.restoring') _i18n_t='Restaurando desde {archive}...' ;;
  'es:backup.cli.restoring_data') _i18n_t='   restaurando el directorio de datos...' ;;
  'es:backup.cli.restoring_db') _i18n_t='   restaurando la base de Postgres...' ;;
  'es:backup.cli.secretbox_reminder') _i18n_t='   El archivo no incluye OCTOPUS_SECRETBOX_KEY. Guardá esa clave aparte: sin ella las credenciales del volcado no se pueden volver a leer.' ;;
  'es:backup.errors.compose_file_missing') _i18n_t='El archivo de compose '\''{file}'\'' no existe.' ;;
  'es:backup.errors.data_dir_missing') _i18n_t='El directorio de datos {dir} no existe. No se respaldó nada: apuntá OCTOPUS_DATA_DIR al directorio que tiene projects/ y task-attachments/.' ;;
  'es:backup.errors.data_dir_unreadable') _i18n_t='Este usuario no puede leer el directorio de datos {dir}. No se respaldó nada; corrélo como dueño del directorio o como root.' ;;
  'es:backup.errors.database_url_required') _i18n_t='OCTOPUS_DATABASE_URL es obligatorio (DSN postgres://).' ;;
  'es:backup.errors.empty_dump') _i18n_t='pg_dump no escribió nada, así que no se armó ningún archivo.' ;;
  'es:backup.errors.no_data_source') _i18n_t='No hay directorio de datos para respaldar. Definí OCTOPUS_DATA_DIR (un directorio de esta máquina), OCTOPUS_COMPOSE_FILE (el archivo del stack de compose) u OCTOPUS_BACKUP_SOURCE=kubernetes.' ;;
  'es:backup.errors.pvc_path') _i18n_t='No se encontró dónde guarda sus archivos el PVC {pvc} (namespace {namespace}). Corrélo en el nodo de k3s o definí OCTOPUS_DATA_DIR con el directorio del volumen.' ;;
  'es:backup.errors.remote_failed') _i18n_t='No se pudo copiar el backup a {remote}. El archivo local {archive} está completo.' ;;
  'es:backup.errors.restore_usage') _i18n_t='Uso: RESTORE=1 ./backup.sh <archivo.tar.gz>' ;;
  'es:backup.errors.status_not_written') _i18n_t='No se pudo escribir {file}; Configuración → Actualizaciones no va a mostrar este backup. El archivo está completo.' ;;
  'es:backup.errors.unknown_source') _i18n_t='OCTOPUS_BACKUP_SOURCE tiene que ser host, compose o kubernetes, y vale '\''{source}'\''.' ;;
  'es:common.errors.not_on_path') _i18n_t=''\''{tool}'\'' no está en el PATH.' ;;
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

BACKUP_DIR="${1:-./backups}"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
ARCHIVE="${BACKUP_DIR}/octopus_${TIMESTAMP}.tar.gz"
K8S_NS="${OCTOPUS_K8S_NAMESPACE:-octopus}"
K8S_PVC="${OCTOPUS_K8S_PVC:-octopus-octopus}"
K8S_PG="${OCTOPUS_K8S_POSTGRES:-statefulset/octopus-postgres}"
PG_USER="${OCTOPUS_PG_USER:-octopus}"
PG_DB="${OCTOPUS_PG_DB:-octopus}"
DUMP_FLAGS=(--clean --if-exists --no-owner --no-acl)
STATUS_FILE=backup-status.json

need() {
  command -v "$1" >/dev/null 2>&1 || {
    msg common.errors.not_on_path tool "$1" >&2
    exit 1
  }
}

compose() {
  docker compose -f "$OCTOPUS_COMPOSE_FILE" "$@"
}

SOURCE="${OCTOPUS_BACKUP_SOURCE:-}"
if [ -z "$SOURCE" ]; then
  if [ -n "${OCTOPUS_DATA_DIR:-}" ]; then
    SOURCE=host
  elif [ -n "${OCTOPUS_COMPOSE_FILE:-}" ]; then
    SOURCE=compose
  elif [ -d ./data ]; then
    SOURCE=host
    OCTOPUS_DATA_DIR=./data
  else
    msg backup.errors.no_data_source >&2
    exit 1
  fi
fi

DATA_DIR=""
case "$SOURCE" in
host)
  DATA_DIR="${OCTOPUS_DATA_DIR:-}"
  if [ -z "$DATA_DIR" ]; then
    msg backup.errors.no_data_source >&2
    exit 1
  fi
  ;;
compose)
  if [ -z "${OCTOPUS_COMPOSE_FILE:-}" ] || [ ! -f "$OCTOPUS_COMPOSE_FILE" ]; then
    msg backup.errors.compose_file_missing file "${OCTOPUS_COMPOSE_FILE:-}" >&2
    exit 1
  fi
  need docker
  ;;
kubernetes)
  need kubectl
  DATA_DIR="${OCTOPUS_DATA_DIR:-}"
  if [ -z "$DATA_DIR" ]; then
    pv=$(kubectl -n "$K8S_NS" get pvc "$K8S_PVC" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
    if [ -n "$pv" ]; then
      DATA_DIR=$(kubectl get pv "$pv" -o jsonpath='{.spec.hostPath.path}{.spec.local.path}' 2>/dev/null || true)
    fi
    if [ -z "$DATA_DIR" ]; then
      msg backup.errors.pvc_path pvc "$K8S_PVC" namespace "$K8S_NS" >&2
      exit 1
    fi
  fi
  ;;
*)
  msg backup.errors.unknown_source source "$SOURCE" >&2
  exit 1
  ;;
esac

check_data_dir() {
  if [ ! -d "$DATA_DIR" ]; then
    msg backup.errors.data_dir_missing dir "$DATA_DIR" >&2
    exit 1
  fi
  if [ ! -r "$DATA_DIR" ] || [ ! -x "$DATA_DIR" ]; then
    msg backup.errors.data_dir_unreadable dir "$DATA_DIR" >&2
    exit 1
  fi
}

use_dsn() {
  case "$SOURCE" in
  host)
    if [ -z "${OCTOPUS_DATABASE_URL:-}" ]; then
      msg backup.errors.database_url_required >&2
      exit 1
    fi
    need "$1"
    return 0
    ;;
  compose)
    [ -n "${OCTOPUS_DATABASE_URL:-}" ] && command -v "$1" >/dev/null 2>&1
    ;;
  *) return 1 ;;
  esac
}

dump_db() {
  if use_dsn pg_dump; then
    pg_dump "${DUMP_FLAGS[@]}" "$OCTOPUS_DATABASE_URL"
  elif [ "$SOURCE" = compose ]; then
    compose exec -T pg pg_dump "${DUMP_FLAGS[@]}" -U "$PG_USER" "$PG_DB"
  else
    kubectl -n "$K8S_NS" exec -i "$K8S_PG" -- pg_dump "${DUMP_FLAGS[@]}" -U "$PG_USER" "$PG_DB"
  fi
}

restore_db() {
  if use_dsn psql; then
    psql "$OCTOPUS_DATABASE_URL" -v ON_ERROR_STOP=1 -f "$1"
  elif [ "$SOURCE" = compose ]; then
    compose exec -T pg psql -v ON_ERROR_STOP=1 -U "$PG_USER" -d "$PG_DB" <"$1"
  else
    kubectl -n "$K8S_NS" exec -i "$K8S_PG" -- psql -v ON_ERROR_STOP=1 -U "$PG_USER" -d "$PG_DB" <"$1"
  fi
}

copy_data() {
  if [ "$SOURCE" = compose ]; then
    # busybox tar in the daemon image may lack --exclude, so entries are listed by hand.
    # shellcheck disable=SC2016 # i18n-ignore the script runs, and expands, inside the container
    inner='cd /data && set -- && for e in * .[!.]*; do [ -e "$e" ] || continue; case $e in logs|runs|backup-status.json) ;; *) set -- "$@" "$e" ;; esac; done && if [ $# -gt 0 ]; then tar -cf - "$@"; fi'
    compose run --rm --no-deps -T --entrypoint sh octopus -c "$inner" >"$TEMP_DIR/data.tar"
    if [ -s "$TEMP_DIR/data.tar" ]; then
      # -p: keep archived modes; a non-root tar would apply umask 077.
      tar -xpf "$TEMP_DIR/data.tar" -C "$1"
    fi
    rm -f "$TEMP_DIR/data.tar"
    return
  fi
  for entry in "$DATA_DIR"/* "$DATA_DIR"/.[!.]*; do
    [ -e "$entry" ] || continue
    case "$(basename "$entry")" in
    logs | runs | "$STATUS_FILE") continue ;;
    esac
    cp -a "$entry" "$1/"
  done
}

# Copy entry by entry: copying "$1/." would stamp the staging dir's owner and mode onto the data dir
# and lock out the daemon.
restore_data() {
  local entries=() entry
  for entry in "$1"/* "$1"/.[!.]*; do
    [ -e "$entry" ] && entries+=("./$(basename "$entry")")
  done
  [ ${#entries[@]} -gt 0 ] || return 0
  if [ "$SOURCE" = compose ]; then
    tar -cf - -C "$1" "${entries[@]}" | compose run --rm --no-deps -T --entrypoint tar octopus -xf - -C /data
    return
  fi
  mkdir -p "$DATA_DIR"
  for entry in "${entries[@]}"; do
    cp -a "$1/$entry" "$DATA_DIR/"
  done
}

db_label() {
  case "$SOURCE" in
  host) printf '%s' "${OCTOPUS_DATABASE_URL:-}" ;;
  compose) if use_dsn psql; then printf '%s' "$OCTOPUS_DATABASE_URL"; else printf '%s' "$OCTOPUS_COMPOSE_FILE:pg"; fi ;;
  kubernetes) printf '%s' "$K8S_NS/$K8S_PG" ;;
  esac
}
data_label() {
  if [ "$SOURCE" = compose ]; then
    printf '%s' "$OCTOPUS_COMPOSE_FILE:octopus:/data"  # i18n-ignore a compose service and path
  else
    printf '%s' "$DATA_DIR"
  fi
}

json_str() {
  local s=${1//\\/\\\\}
  s=${s//\"/\\\"}
  printf '"%s"' "$s"
}

abs_path() {
  if [ -d "$1" ]; then
    (cd "$1" && pwd)
  else
    printf '%s/%s' "$(cd "$(dirname "$1")" && pwd)" "$(basename "$1")"
  fi
}

# OCTOPUS_DATABASE_URL is left out on purpose: it holds a password and the daemon can read this
# file.
restore_env() {
  case "$SOURCE" in
  host) printf 'OCTOPUS_DATA_DIR=%s\n' "$(abs_path "$DATA_DIR")" ;;
  compose) printf 'OCTOPUS_COMPOSE_FILE=%s\n' "$(abs_path "$OCTOPUS_COMPOSE_FILE")" ;;
  kubernetes)
    printf 'OCTOPUS_BACKUP_SOURCE=kubernetes\n'
    [ -n "${OCTOPUS_K8S_NAMESPACE:-}" ] && printf 'OCTOPUS_K8S_NAMESPACE=%s\n' "$K8S_NS"
    [ -n "${OCTOPUS_K8S_PVC:-}" ] && printf 'OCTOPUS_K8S_PVC=%s\n' "$K8S_PVC"
    [ -n "${OCTOPUS_DATA_DIR:-}" ] && printf 'OCTOPUS_DATA_DIR=%s\n' "$(abs_path "$DATA_DIR")"
    return 0
    ;;
  esac
}

write_status() {
  local env_json="" line body database_url=false
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    env_json="$env_json${env_json:+,}$(json_str "$line")"
  done < <(restore_env)
  if use_dsn pg_dump; then
    database_url=true
  fi
  # i18n-ignore a JSON body, not a message
  body=$(printf '{"archive":%s,"backup_dir":%s,"size_bytes":%s,"finished_at":%s,"kept":%s,"keep":%s,"source":%s,"script":%s,"restore_env":[%s],"database_url":%s,"remote":%s,"remote_state":%s}' \
    "$(json_str "$(basename "$ARCHIVE")")" "$(json_str "$(abs_path "$BACKUP_DIR")")" \
    "$ARCHIVE_BYTES" "$(json_str "$FINISHED_AT")" "$REMAINING" "$KEEP" \
    "$(json_str "$SOURCE")" "$(json_str "$(abs_path "$0")")" "$env_json" "$database_url" \
    "$(json_str "$REMOTE")" "$(json_str "$1")")
  if [ "$SOURCE" = compose ]; then
    printf '%s\n' "$body" | compose run --rm --no-deps -T --entrypoint sh octopus -c "cat >/data/$STATUS_FILE" ||
      msg backup.errors.status_not_written file "$STATUS_FILE" >&2
    return 0
  fi
  if printf '%s\n' "$body" >"$DATA_DIR/$STATUS_FILE.tmp" &&
    chmod 644 "$DATA_DIR/$STATUS_FILE.tmp" && mv "$DATA_DIR/$STATUS_FILE.tmp" "$DATA_DIR/$STATUS_FILE"; then
    return 0
  fi
  rm -f "$DATA_DIR/$STATUS_FILE.tmp"
  msg backup.errors.status_not_written file "$DATA_DIR/$STATUS_FILE" >&2
}

if [ "${RESTORE:-}" = "1" ]; then
  if [ -z "${1:-}" ]; then
    msg backup.errors.restore_usage >&2
    exit 1
  fi
  ARCHIVE_PATH="$1"
  echo "==> $(msg backup.cli.restoring archive "$ARCHIVE_PATH")"
  msg backup.cli.restore_warning database "$(db_label)" dir "$(data_label)"
  read -rp "$(msg backup.cli.confirm)" confirm
  [[ "$confirm" =~ ^[YySs]$ ]] || exit 1

  TMP_RESTORE=$(mktemp -d "$(dirname "$ARCHIVE_PATH")/.octopus_restore.XXXXXX" 2>/dev/null || mktemp -d)
  trap 'rm -rf "$TMP_RESTORE"' EXIT
  # -p: keep archived modes; a non-root tar would apply umask 077.
  tar -xpzf "$ARCHIVE_PATH" -C "$TMP_RESTORE"

  if [ -f "$TMP_RESTORE/postgres.sql" ]; then
    msg backup.cli.restoring_db
    restore_db "$TMP_RESTORE/postgres.sql"
  fi
  if [ -d "$TMP_RESTORE/data" ]; then
    msg backup.cli.restoring_data
    restore_data "$TMP_RESTORE/data"
  fi
  echo "==> $(msg backup.cli.restore_complete)"
  exit 0
fi

if [ "$SOURCE" != compose ]; then
  check_data_dir
fi
mkdir -p "$BACKUP_DIR"
echo "==> $(msg backup.cli.backing_up)"

# Staged next to the archive, not in $TMPDIR: /tmp may be RAM or a small / that the copy would fill.
TEMP_DIR=$(mktemp -d "$BACKUP_DIR/.octopus_backup.XXXXXX")
trap 'rm -rf "$TEMP_DIR"' EXIT

msg backup.cli.dumping_db
dump_db >"$TEMP_DIR/postgres.sql"
if [ ! -s "$TEMP_DIR/postgres.sql" ]; then
  msg backup.errors.empty_dump >&2
  exit 1
fi

msg backup.cli.copying_data from "$(data_label)"
mkdir -p "$TEMP_DIR/data"
copy_data "$TEMP_DIR/data"

# i18n-ignore a JSON body, not a message
printf '{"timestamp":"%s","source":"%s","data_dir":"%s"}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$SOURCE" "$(data_label)" >"$TEMP_DIR/backup_meta.json"

tar -czf "$ARCHIVE" -C "$TEMP_DIR" postgres.sql data backup_meta.json
chmod 600 "$ARCHIVE"
ARCHIVE_SIZE=$(du -sh "$ARCHIVE" | cut -f1)
ARCHIVE_BYTES=$(wc -c <"$ARCHIVE" | tr -d ' ')
FINISHED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "==> $(msg backup.cli.backup_complete archive "$ARCHIVE" size "$ARCHIVE_SIZE")"
msg backup.cli.secretbox_reminder

# Snapshots named in deploy/.octopus-update.json or OCTOPUS_KEPT_SNAPSHOTS are never pruned:
# `thulu update -to X -restore` restores exactly those files.
KEEP="${OCTOPUS_BACKUP_KEEP:-}"
ENV_FILE="$(dirname "$0")/.env"
if [ -z "$KEEP" ] && [ -f "$ENV_FILE" ]; then
  KEEP=$(sed -n 's/^OCTOPUS_BACKUP_KEEP=//p' "$ENV_FILE" | tail -n 1 | tr -d "\"' ")
fi
case "$KEEP" in
  '' | *[!0-9]* | 0) KEEP=5 ;;
esac
SPARED=""
STATE_FILE="$(dirname "$0")/.octopus-update.json"
if [ -f "$STATE_FILE" ]; then
  while IFS= read -r p; do
    [ -n "$p" ] && SPARED="$SPARED$(basename "$p")"$'\n'
  done < <(grep -o '"\(backup_path\|snapshot\)"[[:space:]]*:[[:space:]]*"[^"]*"' "$STATE_FILE" |
    sed 's/.*:[[:space:]]*"\([^"]*\)"$/\1/')
fi
KEPT_SNAPSHOTS="${OCTOPUS_KEPT_SNAPSHOTS:-}"
if [ -z "$KEPT_SNAPSHOTS" ] && [ -f "$ENV_FILE" ]; then
  KEPT_SNAPSHOTS=$(sed -n 's/^OCTOPUS_KEPT_SNAPSHOTS=//p' "$ENV_FILE" | tail -n 1 | tr -d "\"' ")
fi
while IFS= read -r pair; do
  [ -n "$pair" ] && SPARED="$SPARED$(basename "${pair#*=}")"$'\n'
done < <(printf '%s\n' "$KEPT_SNAPSHOTS" | tr ',' '\n')
spared() {
  printf '%s' "$SPARED" | grep -qxF -- "$1"
}
PRUNED=0
KEPT=0
SNAPSHOTS=()
for path in "$BACKUP_DIR"/octopus_*.tar.gz "$BACKUP_DIR"/octopus_*.sql; do
  if [ -f "$path" ]; then
    SNAPSHOTS+=("$(basename "$path")")
  fi
done
while IFS= read -r name; do
  if [ -z "$name" ] || spared "$name"; then
    continue
  fi
  KEPT=$((KEPT + 1))
  if [ "$KEPT" -gt "$KEEP" ]; then
    rm -f "$BACKUP_DIR/$name"
    PRUNED=$((PRUNED + 1))
  fi
done < <(printf '%s\n' ${SNAPSHOTS[@]+"${SNAPSHOTS[@]}"} | sort -r)
if [ "$PRUNED" -gt 0 ]; then
  msg backup.cli.pruned count "$PRUNED" keep "$KEEP"
fi
REMAINING=$((${#SNAPSHOTS[@]} - PRUNED))

REMOTE="${OCTOPUS_BACKUP_REMOTE:-}"
if [ -n "$REMOTE" ]; then
  write_status pending
else
  write_status none
fi

if [ -n "$REMOTE" ]; then
  need rsync
  need ssh
  REMOTE_HOST="${REMOTE%%:*}"
  REMOTE_DIR="${REMOTE#*:}"
  if [ "$REMOTE_DIR" = "$REMOTE" ] || [ -z "$REMOTE_DIR" ]; then
    REMOTE_DIR=.
  fi
  # i18n-ignore an ssh command line
  REMOTE_SSH="${OCTOPUS_BACKUP_REMOTE_SSH:-ssh -o BatchMode=yes}"
  read -r -a SSH_ARGV <<<"$REMOTE_SSH"
  REMOTE_KEEP="${OCTOPUS_BACKUP_REMOTE_KEEP:-}"
  case "$REMOTE_KEEP" in
    '' | *[!0-9]* | 0) REMOTE_KEEP=14 ;;
  esac

  UPLOAD="$ARCHIVE"
  if [ -n "${OCTOPUS_BACKUP_AGE_RECIPIENT:-}" ]; then
    need age
    msg backup.cli.encrypting recipient "$OCTOPUS_BACKUP_AGE_RECIPIENT"
    UPLOAD="$TEMP_DIR/$(basename "$ARCHIVE").age"
    age -r "$OCTOPUS_BACKUP_AGE_RECIPIENT" -o "$UPLOAD" "$ARCHIVE"
  fi

  remote_failed() {
    msg backup.errors.remote_failed remote "$REMOTE" archive "$ARCHIVE" >&2
    write_status failed
    exit 1
  }
  msg backup.cli.pushing remote "$REMOTE"
  # Restricted backup shells have mkdir but not -p.
  "${SSH_ARGV[@]}" "$REMOTE_HOST" mkdir "$REMOTE_DIR" >/dev/null 2>&1 || true
  rsync -e "$REMOTE_SSH" --chmod=F600 "$UPLOAD" "$REMOTE_HOST:$REMOTE_DIR/" || remote_failed

  # rsync --list-only works on restricted shells where ls does not.
  LISTING=$(rsync -e "$REMOTE_SSH" --list-only "$REMOTE_HOST:$REMOTE_DIR/") || remote_failed
  REMOTE_PRUNED=0
  REMOTE_KEPT=0
  while IFS= read -r name; do
    case "$name" in
    octopus_*.tar.gz | octopus_*.tar.gz.age) ;;
    *) continue ;;
    esac
    REMOTE_KEPT=$((REMOTE_KEPT + 1))
    if [ "$REMOTE_KEPT" -gt "$REMOTE_KEEP" ]; then
      "${SSH_ARGV[@]}" "$REMOTE_HOST" rm "$REMOTE_DIR/$name" || remote_failed
      REMOTE_PRUNED=$((REMOTE_PRUNED + 1))
    fi
  done < <(printf '%s\n' "$LISTING" | awk '{print $NF}' | sort -r)
  if [ "$REMOTE_PRUNED" -gt 0 ]; then
    msg backup.cli.remote_pruned count "$REMOTE_PRUNED" keep "$REMOTE_KEEP"
  fi
  write_status ok
fi
