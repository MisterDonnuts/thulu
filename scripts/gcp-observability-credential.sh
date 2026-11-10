#!/usr/bin/env bash
# The role is the only boundary: thulu-cloud uses the cloud-platform scope, so every permission
# granted here must be a read.
set -euo pipefail

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
  'en:cloud_credentials.gcp.binding') _i18n_t='Binding {role} to {name}' ;;
  'en:cloud_credentials.gcp.creating_role:one') _i18n_t='Creating role {role} ({count} permission)' ;;
  'en:cloud_credentials.gcp.creating_role:other') _i18n_t='Creating role {role} ({count} permissions)' ;;
  'en:cloud_credentials.gcp.creating_sa') _i18n_t='Creating service account {name}' ;;
  'en:cloud_credentials.gcp.enabling_apis') _i18n_t='Enabling APIs: {apis}' ;;
  'en:cloud_credentials.gcp.key_written') _i18n_t='Key written to {file} (mode 0600). Not printed here on purpose.' ;;
  'en:cloud_credentials.gcp.next_steps') _i18n_t='
Next — load it by hand, which is the point of stopping here:
  1. open the key file:            open -e {file}
  2. Credentials → New credential → Google Cloud → Service account key
  3. paste the WHOLE file into "Key file (JSON)". Leave Project ID empty unless
     the permissions should be checked against a project other than the key'\''s.
  4. Reachable from → pick the ENVIRONMENT this project belongs to. A cloud key
     is about an environment, so granting it anywhere else either over-shares it
     or leaves it unreachable. The environment'\''s provider must be `gcp`, or
     thulu-cloud refuses the pair rather than guessing.
  5. Validate & save — you should get {email} back as the identity, and a
     tool matrix showing which thulu-cloud tools this key can actually drive.

Housekeeping:
  • Delete {file} once Thulu has accepted the key — it is encrypted at
    rest there, and this file is a second copy with none of those protections:
      rm {file}
  • Google allows 10 keys per service account. To retire an old one:
      gcloud iam service-accounts keys list --iam-account={email}
      gcloud iam service-accounts keys delete <KEY_ID> --iam-account={email}
  • To see what this account can do, at any time:
      gcloud iam roles describe {role} --project={project}

If key creation was refused, the org policy
constraints/iam.disableServiceAccountKeyCreation is on and only an org admin can
grant an exception for this account.' ;;
  'en:cloud_credentials.gcp.no_project') _i18n_t='no project. Pass --project <id> or run '\''gcloud config set project <id>'\''.' ;;
  'en:cloud_credentials.gcp.project') _i18n_t='Project:         {project}' ;;
  'en:cloud_credentials.gcp.reusing_sa') _i18n_t='Reusing service account {name}' ;;
  'en:cloud_credentials.gcp.role_description') _i18n_t='Exactly the read calls tools/thulu-cloud/server.js makes.' ;;
  'en:cloud_credentials.gcp.role_title') _i18n_t='Thulu observability (read-only)' ;;
  'en:cloud_credentials.gcp.sa_description') _i18n_t='Read-only telemetry for Thulu/thulu-cloud' ;;
  'en:cloud_credentials.gcp.sa_display_name') _i18n_t='Thulu observability' ;;
  'en:cloud_credentials.gcp.service_account') _i18n_t='Service account: {email}' ;;
  'en:cloud_credentials.gcp.skipping_apis') _i18n_t='Skipping API enablement (--no-enable-apis)' ;;
  'en:cloud_credentials.gcp.updating_role:one') _i18n_t='Updating role {role} ({count} permission)' ;;
  'en:cloud_credentials.gcp.updating_role:other') _i18n_t='Updating role {role} ({count} permissions)' ;;
  'en:cloud_credentials.gcp.usage') _i18n_t='usage: scripts/gcp-observability-credential.sh [options]

options:
  --project <id>     GCP project (default: gcloud config get-value project)
  --sa <name>        Service-account name  (default: octopus-observability)
  --role <id>        Custom role id        (default: octopusObservabilityRead)
  --keyfile <path>   Where to write the key
                     (default: ~/.octopus/gcp-<sa>-<project>.json)
  --metrics-only     Grant ONLY monitoring.timeSeries.list. The dashboard'\''s
                     metrics still collect, but logs_search, error_groups,
                     deploys and discover_resources all lose their permission
                     and Thulu will show them as refused in the tool matrix.
  --no-enable-apis   Skip `gcloud services enable`. Use when the APIs are
                     already on, or when you lack serviceusage.services.enable
                     and someone else has to do it.' ;;
  'en:common.errors.not_on_path') _i18n_t=''\''{tool}'\'' is not on PATH.' ;;
  'en:common.errors.unknown_argument') _i18n_t='unknown argument '\''{arg}'\'' (see --help)' ;;
  'es:cloud_credentials.gcp.binding') _i18n_t='Asociando {role} a {name}' ;;
  'es:cloud_credentials.gcp.creating_role:one') _i18n_t='Creando el rol {role} ({count} permiso)' ;;
  'es:cloud_credentials.gcp.creating_role:other') _i18n_t='Creando el rol {role} ({count} permisos)' ;;
  'es:cloud_credentials.gcp.creating_sa') _i18n_t='Creando la service account {name}' ;;
  'es:cloud_credentials.gcp.enabling_apis') _i18n_t='Habilitando APIs: {apis}' ;;
  'es:cloud_credentials.gcp.key_written') _i18n_t='Key escrita en {file} (modo 0600). A propósito no se muestra acá.' ;;
  'es:cloud_credentials.gcp.next_steps') _i18n_t='
Siguiente — cargala a mano, para eso se frena acá:
  1. abrí el archivo de la key:    open -e {file}
  2. Credentials → New credential → Google Cloud → Service account key
  3. pegá el archivo ENTERO en "Key file (JSON)". Dejá Project ID vacío salvo
     que los permisos se tengan que verificar contra otro proyecto que el de la key.
  4. Reachable from → elegí el ENTORNO al que pertenece este proyecto. Una key de
     cloud es de un entorno, así que concederla en otro lado o la sobreexpone o
     la deja inalcanzable. El provider del entorno tiene que ser `gcp`, o
     thulu-cloud rechaza el par en vez de adivinar.
  5. Validate & save — tenés que recibir {email} como identidad, y una
     matriz de tools que muestra qué tools de thulu-cloud puede manejar esta key.

Limpieza:
  • Borrá {file} cuando Thulu haya aceptado la key — allá queda cifrada en
    reposo, y este archivo es una segunda copia sin ninguna de esas protecciones:
      rm {file}
  • Google permite 10 keys por service account. Para dar de baja una vieja:
      gcloud iam service-accounts keys list --iam-account={email}
      gcloud iam service-accounts keys delete <KEY_ID> --iam-account={email}
  • Para ver qué puede hacer esta cuenta, cuando quieras:
      gcloud iam roles describe {role} --project={project}

Si se rechazó la creación de la key, está prendida la org policy
constraints/iam.disableServiceAccountKeyCreation y sólo un admin de la
organización puede dar una excepción para esta cuenta.' ;;
  'es:cloud_credentials.gcp.no_project') _i18n_t='no hay proyecto. Pasá --project <id> o corré '\''gcloud config set project <id>'\''.' ;;
  'es:cloud_credentials.gcp.project') _i18n_t='Proyecto:        {project}' ;;
  'es:cloud_credentials.gcp.reusing_sa') _i18n_t='Se reusa la service account {name}' ;;
  'es:cloud_credentials.gcp.role_description') _i18n_t='Exactly the read calls tools/thulu-cloud/server.js makes.' ;;
  'es:cloud_credentials.gcp.role_title') _i18n_t='Thulu observability (read-only)' ;;
  'es:cloud_credentials.gcp.sa_description') _i18n_t='Read-only telemetry for Thulu/thulu-cloud' ;;
  'es:cloud_credentials.gcp.sa_display_name') _i18n_t='Thulu observability' ;;
  'es:cloud_credentials.gcp.service_account') _i18n_t='Service account: {email}' ;;
  'es:cloud_credentials.gcp.skipping_apis') _i18n_t='Se saltea la habilitación de APIs (--no-enable-apis)' ;;
  'es:cloud_credentials.gcp.updating_role:one') _i18n_t='Actualizando el rol {role} ({count} permiso)' ;;
  'es:cloud_credentials.gcp.updating_role:other') _i18n_t='Actualizando el rol {role} ({count} permisos)' ;;
  'es:cloud_credentials.gcp.usage') _i18n_t='uso: scripts/gcp-observability-credential.sh [opciones]

opciones:
  --project <id>     proyecto de GCP (por defecto: gcloud config get-value project)
  --sa <nombre>      nombre de la service account  (por defecto: octopus-observability)
  --role <id>        id del rol custom             (por defecto: octopusObservabilityRead)
  --keyfile <ruta>   dónde escribir la key
                     (por defecto: ~/.octopus/gcp-<sa>-<project>.json)
  --metrics-only     concede SÓLO monitoring.timeSeries.list. Las métricas del
                     dashboard se siguen juntando, pero logs_search, error_groups,
                     deploys y discover_resources pierden su permiso y Thulu
                     los muestra como rechazados en la matriz de tools.
  --no-enable-apis   saltea `gcloud services enable`. Usalo cuando las APIs ya
                     están prendidas, o cuando no tenés serviceusage.services.enable
                     y lo tiene que hacer otra persona.' ;;
  'es:common.errors.not_on_path') _i18n_t=''\''{tool}'\'' no está en el PATH.' ;;
  'es:common.errors.unknown_argument') _i18n_t='argumento desconocido '\''{arg}'\'' (mirá --help)' ;;
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

SA_NAME="octopus-observability"
ROLE_ID="octopusObservabilityRead"
PROJECT=""
KEYFILE=""
ENABLE_APIS=1
METRICS_ONLY=0

usage() {
  msg cloud_credentials.gcp.usage >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --project) PROJECT="${2:-}"; shift 2 ;;
    --sa) SA_NAME="${2:-}"; shift 2 ;;
    --role) ROLE_ID="${2:-}"; shift 2 ;;
    --keyfile) KEYFILE="${2:-}"; shift 2 ;;
    --metrics-only) METRICS_ONLY=1; shift ;;
    --no-enable-apis) ENABLE_APIS=0; shift ;;
    -h|--help) usage ;;
    *) echo "ERROR: $(msg common.errors.unknown_argument arg "$1")" >&2; usage ;;
  esac
done

command -v gcloud >/dev/null 2>&1 || { echo "ERROR: $(msg common.errors.not_on_path tool gcloud)" >&2; exit 1; }

if [ -z "$PROJECT" ]; then
  PROJECT="$(gcloud config get-value project 2>/dev/null || true)"
fi
if [ -z "$PROJECT" ] || [ "$PROJECT" = "(unset)" ]; then
  echo "ERROR: $(msg cloud_credentials.gcp.no_project)" >&2
  exit 1
fi

: "${KEYFILE:=$HOME/.octopus/gcp-${SA_NAME}-${PROJECT}.json}"

SA_EMAIL="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com"
ROLE_REF="projects/${PROJECT}/roles/${ROLE_ID}"

echo "==> $(msg cloud_credentials.gcp.project project "$PROJECT")"
echo "==> $(msg cloud_credentials.gcp.service_account email "$SA_EMAIL")"

# CORE_PERMS must match what internal/credentials/catalog.go declares for thulu-cloud's tools.
CORE_PERMS=(
  monitoring.timeSeries.list   # metrics_query — the dashboard's collector
  logging.logEntries.list      # logs_search, error_groups
  run.services.list            # deploys, and Cloud Run discovery
)

DISCOVERY_PERMS=(
  logging.logs.list            # which log names exist
  errorreporting.groups.list   # error_groups' preferred source
  run.revisions.list           # deploy markers are revision creations
  compute.urlMaps.list         # HTTP(S) load balancers
  container.clusters.list      # GKE
  cloudsql.instances.list      # Cloud SQL
  cloudfunctions.functions.list
)

if [ "$METRICS_ONLY" = 1 ]; then
  PERMS=(monitoring.timeSeries.list)
else
  PERMS=("${CORE_PERMS[@]}" "${DISCOVERY_PERMS[@]}")
fi
PERM_CSV="$(IFS=,; echo "${PERMS[*]}")"

# cloudresourcemanager is required: capability discovery calls testIamPermissions on it, and an
# undiscovered key is treated as capable of everything.
# Discovery-only APIs stay off on purpose; an off API shows as a gap in discover_resources.
if [ "$ENABLE_APIS" = 1 ]; then
  APIS=(cloudresourcemanager.googleapis.com iam.googleapis.com monitoring.googleapis.com)
  if [ "$METRICS_ONLY" != 1 ]; then
    APIS+=(logging.googleapis.com clouderrorreporting.googleapis.com run.googleapis.com)
  fi
  echo "==> $(msg cloud_credentials.gcp.enabling_apis apis "${APIS[*]}")"
  gcloud services enable "${APIS[@]}" --project="$PROJECT" --quiet
else
  echo "==> $(msg cloud_credentials.gcp.skipping_apis)"
fi

if gcloud iam roles describe "$ROLE_ID" --project="$PROJECT" >/dev/null 2>&1; then
  echo "==> $(msg cloud_credentials.gcp.updating_role role "$ROLE_ID" count "${#PERMS[@]}")"
  gcloud iam roles update "$ROLE_ID" --project="$PROJECT" \
    --permissions="$PERM_CSV" --stage=GA --quiet >/dev/null
else
  echo "==> $(msg cloud_credentials.gcp.creating_role role "$ROLE_ID" count "${#PERMS[@]}")"
  gcloud iam roles create "$ROLE_ID" --project="$PROJECT" \
    --title="$(msg_en cloud_credentials.gcp.role_title)" \
    --description="$(msg_en cloud_credentials.gcp.role_description)" \
    --permissions="$PERM_CSV" --stage=GA --quiet >/dev/null
fi

if gcloud iam service-accounts describe "$SA_EMAIL" --project="$PROJECT" >/dev/null 2>&1; then
  echo "==> $(msg cloud_credentials.gcp.reusing_sa name "$SA_NAME")"
else
  echo "==> $(msg cloud_credentials.gcp.creating_sa name "$SA_NAME")"
  gcloud iam service-accounts create "$SA_NAME" --project="$PROJECT" \
    --display-name="$(msg_en cloud_credentials.gcp.sa_display_name)" \
    --description="$(msg_en cloud_credentials.gcp.sa_description)" --quiet >/dev/null
fi

echo "==> $(msg cloud_credentials.gcp.binding role "$ROLE_ID" name "$SA_NAME")"
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member="serviceAccount:${SA_EMAIL}" --role="$ROLE_REF" \
  --condition=None --quiet >/dev/null

# gcloud writes the key straight to the file so it never reaches a shell variable, ps or history;
# umask 077 closes the creation window.
mkdir -p "$(dirname "$KEYFILE")"
( umask 077; gcloud iam service-accounts keys create "$KEYFILE" \
    --iam-account="$SA_EMAIL" --project="$PROJECT" --quiet )
chmod 600 "$KEYFILE"

echo "==> $(msg cloud_credentials.gcp.key_written file "$KEYFILE")"

msg cloud_credentials.gcp.next_steps file "$KEYFILE" email "$SA_EMAIL" role "$ROLE_ID" project "$PROJECT"
