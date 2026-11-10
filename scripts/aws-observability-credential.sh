#!/usr/bin/env bash
# The secret must never be printed, passed in argv or held in a shell variable:
# the AWS CLI writes it to a file and only render_keyfile reads it.
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
_i18n_tmpl() {
  case $1 in
  'en:cloud_credentials.aws.attached') _i18n_t='Attached {policy} to {user}.' ;;
  'en:cloud_credentials.aws.creating_policy') _i18n_t='Creating policy {policy}…' ;;
  'en:cloud_credentials.aws.creating_user') _i18n_t='Creating user {user} (no console access)…' ;;
  'en:cloud_credentials.aws.identity') _i18n_t='AWS identity: {arn}' ;;
  'en:cloud_credentials.aws.key_written') _i18n_t='Access key written to {file} (mode 0600). Not printed here on purpose.' ;;
  'en:cloud_credentials.aws.keyfile_header') _i18n_t='# Thulu — AWS observability credential
# IAM user: {user}
# Created:  {created}
#
# Paste into: Credentials → New credential → AWS → Access keys
# Field names below match the form exactly.' ;;
  'en:cloud_credentials.aws.next_steps') _i18n_t='
Next — load it by hand, which is the point of stopping here:
  1. open the key file:            open -e {file}
  2. Credentials → New credential → AWS → Access keys
  3. paste access_key_id and secret_access_key
  4. Reachable from → pick the ENVIRONMENT this account belongs to. A cloud key
     is about an environment, so granting it anywhere else either over-shares it
     or leaves it unreachable.
  5. Validate & save — you should get the IAM user'\''s ARN back, and a tool matrix
     showing which thulu-cloud tools this key can actually drive.

Then check:
  • Environment Region matches where the resources live, or discover_resources
    comes back empty and it reads like a permissions problem.
  • The credential shows up under "Recent decisions" once an agent uses it.

Housekeeping:
  • Delete {file} once Thulu has accepted the key — it is encrypted at
    rest there, and this file is a second copy with none of those protections:
      rm {file}
  • AWS allows two access keys per user. To retire an old one:
      aws iam list-access-keys --user-name {user}
      aws iam delete-access-key --user-name {user} --access-key-id <id>' ;;
  'en:cloud_credentials.aws.no_access_key') _i18n_t='the key file has no AccessKey block' ;;
  'en:cloud_credentials.aws.policy_description') _i18n_t='Read-only access for Thulu thulu-cloud' ;;
  'en:cloud_credentials.aws.policy_exists') _i18n_t='Policy {policy} exists; publishing a new default version…' ;;
  'en:cloud_credentials.aws.usage') _i18n_t='usage: scripts/aws-observability-credential.sh [options]

options:
  --aws-profile <p>    AWS CLI profile to create the identity with
  --user <name>        IAM user name          (default: octopus-observability)
  --policy <name>      IAM policy name        (default: OctopusObservabilityRead)
  --keyfile <path>     Where to write the key (default: ~/.octopus/aws-<user>.json)
  --no-introspection   Omit iam:SimulatePrincipalPolicy from the policy. The key
                       still works, but Thulu cannot discover what it can do,
                       and an undiscovered key is treated as able to do
                       everything this layer knows about.' ;;
  'en:cloud_credentials.aws.user_exists') _i18n_t='User {user} already exists, reusing it.' ;;
  'en:common.errors.need_json_tool') _i18n_t='need '\''node'\'' or '\''python3'\'' on PATH to parse JSON.' ;;
  'en:common.errors.not_on_path') _i18n_t=''\''{tool}'\'' is not on PATH.' ;;
  'en:common.errors.unknown_argument') _i18n_t='unknown argument '\''{arg}'\'' (see --help)' ;;
  'es:cloud_credentials.aws.attached') _i18n_t='{policy} asociada a {user}.' ;;
  'es:cloud_credentials.aws.creating_policy') _i18n_t='Creando la policy {policy}…' ;;
  'es:cloud_credentials.aws.creating_user') _i18n_t='Creando el usuario {user} (sin acceso a la consola)…' ;;
  'es:cloud_credentials.aws.identity') _i18n_t='Identidad de AWS: {arn}' ;;
  'es:cloud_credentials.aws.key_written') _i18n_t='Access key escrita en {file} (modo 0600). A propósito no se muestra acá.' ;;
  'es:cloud_credentials.aws.keyfile_header') _i18n_t='# Thulu — credencial de observabilidad de AWS
# Usuario IAM: {user}
# Creada:      {created}
#
# Pegala en: Credentials → New credential → AWS → Access keys
# Los nombres de campo de abajo son los mismos del formulario.' ;;
  'es:cloud_credentials.aws.next_steps') _i18n_t='
Siguiente — cargala a mano, para eso se frena acá:
  1. abrí el archivo de la key:    open -e {file}
  2. Credentials → New credential → AWS → Access keys
  3. pegá access_key_id y secret_access_key
  4. Reachable from → elegí el ENTORNO al que pertenece esta cuenta. Una key de
     cloud es de un entorno, así que concederla en otro lado o la sobreexpone o
     la deja inalcanzable.
  5. Validate & save — tenés que recibir el ARN del usuario IAM, y una matriz de
     tools que muestra qué tools de thulu-cloud puede manejar esta key.

Después revisá:
  • La región del entorno coincide con donde viven los recursos; si no,
    discover_resources vuelve vacío y parece un problema de permisos.
  • La credencial aparece en "Recent decisions" cuando un agente la usa.

Limpieza:
  • Borrá {file} cuando Thulu haya aceptado la key — allá queda cifrada en
    reposo, y este archivo es una segunda copia sin ninguna de esas protecciones:
      rm {file}
  • AWS permite dos access keys por usuario. Para dar de baja una vieja:
      aws iam list-access-keys --user-name {user}
      aws iam delete-access-key --user-name {user} --access-key-id <id>' ;;
  'es:cloud_credentials.aws.no_access_key') _i18n_t='el archivo de la key no tiene el bloque AccessKey' ;;
  'es:cloud_credentials.aws.policy_description') _i18n_t='Read-only access for Thulu thulu-cloud' ;;
  'es:cloud_credentials.aws.policy_exists') _i18n_t='La policy {policy} ya existe; publicando una versión default nueva…' ;;
  'es:cloud_credentials.aws.usage') _i18n_t='uso: scripts/aws-observability-credential.sh [opciones]

opciones:
  --aws-profile <p>    perfil del CLI de AWS con el que crear la identidad
  --user <nombre>      nombre del usuario IAM   (por defecto: octopus-observability)
  --policy <nombre>    nombre de la policy IAM  (por defecto: OctopusObservabilityRead)
  --keyfile <ruta>     dónde escribir la key    (por defecto: ~/.octopus/aws-<user>.json)
  --no-introspection   saca iam:SimulatePrincipalPolicy de la policy. La key
                       sigue andando, pero Thulu no puede descubrir qué puede
                       hacer, y una key sin descubrir se trata como capaz de
                       todo lo que esta capa conoce.' ;;
  'es:cloud_credentials.aws.user_exists') _i18n_t='El usuario {user} ya existe, se reusa.' ;;
  'es:common.errors.need_json_tool') _i18n_t='hace falta '\''node'\'' o '\''python3'\'' en el PATH para leer JSON.' ;;
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

IAM_USER="octopus-observability"
IAM_POLICY="OctopusObservabilityRead"
KEYFILE=""
INTROSPECTION=1
AWS_ARGS=()

usage() {
  msg cloud_credentials.aws.usage >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --aws-profile) AWS_ARGS=(--profile "${2:-}"); shift 2 ;;
    --user) IAM_USER="${2:-}"; shift 2 ;;
    --policy) IAM_POLICY="${2:-}"; shift 2 ;;
    --keyfile) KEYFILE="${2:-}"; shift 2 ;;
    --no-introspection) INTROSPECTION=0; shift ;;
    -h|--help) usage ;;
    *) echo "ERROR: $(msg common.errors.unknown_argument arg "$1")" >&2; usage ;;
  esac
done

: "${KEYFILE:=$HOME/.octopus/aws-${IAM_USER}.json}"

command -v aws >/dev/null 2>&1 || { echo "ERROR: $(msg common.errors.not_on_path tool aws)" >&2; exit 1; }
if command -v node >/dev/null 2>&1; then
  JSON_TOOL=node
elif command -v python3 >/dev/null 2>&1; then
  JSON_TOOL=python3
else
  echo "ERROR: $(msg common.errors.need_json_tool)" >&2
  exit 1
fi

aws_() { aws "${AWS_ARGS[@]+"${AWS_ARGS[@]}"}" "$@"; }

# Resource "*": cloudwatch:GetMetricData and logs:GetQueryResults support no resource-level
# permissions. Actions must be exactly the calls tools/thulu-cloud/server.js makes.
# apigateway:GET is required: Lambda's Errors metric misses handled 500s and 4xx rejected before the
# function runs.
# i18n-ignore an IAM policy document, not a message
POLICY_STATEMENTS='
    { "Sid": "OctopusMetrics", "Effect": "Allow",
      "Action": ["cloudwatch:GetMetricData"], "Resource": "*" },
    { "Sid": "OctopusLogs", "Effect": "Allow",
      "Action": ["logs:DescribeLogGroups", "logs:StartQuery", "logs:GetQueryResults"],
      "Resource": "*" },
    { "Sid": "OctopusDiscovery", "Effect": "Allow",
      "Action": [
        "elasticloadbalancing:DescribeLoadBalancers",
        "elasticloadbalancing:DescribeTargetGroups",
        "ecs:ListClusters",
        "ecs:ListServices",
        "lambda:ListFunctions",
        "rds:DescribeDBInstances"
      ], "Resource": "*" },
    { "Sid": "OctopusDiscoveryApiGateway", "Effect": "Allow",
      "Action": ["apigateway:GET"],
      "Resource": [
        "arn:aws:apigateway:*::/apis",
        "arn:aws:apigateway:*::/apis/*",
        "arn:aws:apigateway:*::/restapis",
        "arn:aws:apigateway:*::/restapis/*"
      ] }'

# Capability discovery simulates this key's permissions (internal/credentials/provider_aws.go);
# without this action nothing is discovered,
# and an undiscovered credential is treated as capable of everything.
if [ "$INTROSPECTION" = 1 ]; then
  # i18n-ignore an IAM policy document, not a message
  POLICY_STATEMENTS="${POLICY_STATEMENTS},
    { \"Sid\": \"OctopusIntrospection\", \"Effect\": \"Allow\",
      \"Action\": [\"iam:SimulatePrincipalPolicy\"], \"Resource\": \"*\" }"
fi

# i18n-ignore an IAM policy document, not a message
POLICY_DOC="{
  \"Version\": \"2012-10-17\",
  \"Statement\": [${POLICY_STATEMENTS}
  ]
}"

echo "==> $(msg cloud_credentials.aws.identity arn "$(aws_ sts get-caller-identity --query 'Arn' --output text)")"

ACCOUNT_ID=$(aws_ sts get-caller-identity --query 'Account' --output text)
POLICY_ARN="arn:aws:iam::${ACCOUNT_ID}:policy/${IAM_POLICY}"

TMP_POLICY=$(mktemp)
trap 'rm -f "$TMP_POLICY"' EXIT
printf '%s' "$POLICY_DOC" > "$TMP_POLICY"

if aws_ iam get-policy --policy-arn "$POLICY_ARN" >/dev/null 2>&1; then
  # Publish a new version instead of reusing, so re-running picks up permissions thulu-cloud gained
  # since.
  echo "==> $(msg cloud_credentials.aws.policy_exists policy "$IAM_POLICY")"
  # AWS caps a managed policy at five versions; delete the oldest non-default first.
  VERSIONS=$(aws_ iam list-policy-versions --policy-arn "$POLICY_ARN" \
    --query 'length(Versions)' --output text 2>/dev/null || echo 0)
  if [ "${VERSIONS:-0}" -ge 5 ]; then
    DROP=$(aws_ iam list-policy-versions --policy-arn "$POLICY_ARN" \
      --query 'sort_by(Versions[?IsDefaultVersion==`false`], &CreateDate)[0].VersionId' \
      --output text)
    [ -n "$DROP" ] && [ "$DROP" != "None" ] &&
      aws_ iam delete-policy-version --policy-arn "$POLICY_ARN" --version-id "$DROP"
  fi
  aws_ iam create-policy-version --policy-arn "$POLICY_ARN" \
    --policy-document "file://${TMP_POLICY}" --set-as-default \
    --query 'PolicyVersion.VersionId' --output text >/dev/null
else
  echo "==> $(msg cloud_credentials.aws.creating_policy policy "$IAM_POLICY")"
  aws_ iam create-policy \
    --policy-name "$IAM_POLICY" \
    --description "$(msg_en cloud_credentials.aws.policy_description)" \
    --policy-document "file://${TMP_POLICY}" \
    --query 'Policy.Arn' --output text >/dev/null
fi

if aws_ iam get-user --user-name "$IAM_USER" >/dev/null 2>&1; then
  echo "==> $(msg cloud_credentials.aws.user_exists user "$IAM_USER")"
else
  echo "==> $(msg cloud_credentials.aws.creating_user user "$IAM_USER")"
  aws_ iam create-user --user-name "$IAM_USER" >/dev/null
fi

aws_ iam attach-user-policy --user-name "$IAM_USER" --policy-arn "$POLICY_ARN"
echo "==> $(msg cloud_credentials.aws.attached policy "$IAM_POLICY" user "$IAM_USER")"

# umask before creating each file, not chmod after: chmod leaves a window where the secret is
# world-readable.
mkdir -p "$(dirname "$KEYFILE")"
RAW_KEY="${KEYFILE}.aws-raw.json"
trap 'rm -f "$TMP_POLICY" "$RAW_KEY"' EXIT

( umask 077; aws_ iam create-access-key --user-name "$IAM_USER" > "$RAW_KEY" )

# created_at reads the key's CreateDate, which is not secret, so the header can
# be rendered here in the reader's language.
created_at() {
  if [ "$JSON_TOOL" = node ]; then
    # i18n-ignore JavaScript, not a message
    node -e '
      const fs = require("fs");
      const k = JSON.parse(fs.readFileSync(process.argv[1], "utf8")).AccessKey || {};
      process.stdout.write(k.CreateDate || "");
    ' "$RAW_KEY"
  else
    # i18n-ignore Python, not a message
    python3 -c '
import sys, json
k = json.load(open(sys.argv[1])).get("AccessKey") or {}
sys.stdout.write(k.get("CreateDate") or "")
' "$RAW_KEY"
  fi
}

# render_keyfile writes the header it is given and then the two fields, named
# as the credential form names them. It exits 1 when the AWS response carries
# no AccessKey block.
render_keyfile() {
  if [ "$JSON_TOOL" = node ]; then
    # i18n-ignore JavaScript, not a message
    node -e '
      const fs = require("fs");
      const k = JSON.parse(fs.readFileSync(process.argv[1], "utf8")).AccessKey;
      if (!k || !k.AccessKeyId || !k.SecretAccessKey) process.exit(1);
      process.stdout.write(process.argv[2] + "\n\n" +
        "access_key_id\n" + k.AccessKeyId + "\n\n" +
        "secret_access_key\n" + k.SecretAccessKey + "\n");
    ' "$RAW_KEY" "$1"
  else
    # i18n-ignore Python, not a message
    python3 -c '
import sys, json
k = json.load(open(sys.argv[1])).get("AccessKey") or {}
if not k.get("AccessKeyId") or not k.get("SecretAccessKey"):
    sys.exit(1)
sys.stdout.write("%s\n\naccess_key_id\n%s\n\nsecret_access_key\n%s\n"
    % (sys.argv[2], k["AccessKeyId"], k["SecretAccessKey"]))
' "$RAW_KEY" "$1"
  fi
}

HEADER=$(msg cloud_credentials.aws.keyfile_header user "$IAM_USER" created "$(created_at)")
if ! ( umask 077; render_keyfile "$HEADER" > "$KEYFILE" ); then
  echo "ERROR: $(msg cloud_credentials.aws.no_access_key)" >&2
  rm -f "$KEYFILE"
  exit 1
fi
rm -f "$RAW_KEY"

echo "==> $(msg cloud_credentials.aws.key_written file "$KEYFILE")"

msg cloud_credentials.aws.next_steps file "$KEYFILE" user "$IAM_USER"
