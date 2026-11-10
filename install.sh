#!/bin/sh
# Everything below is a function and the last line calls main, so a `curl | sh` download cut short
# runs nothing.

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
# shellcheck disable=SC2016 # literal text: a $ or ` in a message is for the reader to type
_i18n_tmpl() {
	case $1 in
	'en:common.errors.unknown_argument') _i18n_t='unknown argument '\''{arg}'\'' (see --help)' ;;
	'en:install.cli.already_installed') _i18n_t='  {tag} is already installed at {path}; nothing to do' ;;
	'en:install.cli.checksum_ok') _i18n_t='  checksum ok' ;;
	'en:install.cli.downloading') _i18n_t='  downloading {asset}' ;;
	'en:install.cli.error') _i18n_t='error: {message}' ;;
	'en:install.cli.header') _i18n_t='Thulu {tag} ({platform})' ;;
	'en:install.cli.init_prompt') _i18n_t='
Configure it now? (thulu init) [Y/n]' ;;
	'en:install.cli.installed') _i18n_t='  installed {path}' ;;
	'en:install.cli.installing_unverified') _i18n_t='  ! {reason}; installing anyway because OCTOPUS_INSECURE_SKIP_VERIFY=1' ;;
	'en:install.cli.next_steps') _i18n_t='
Next, configure and start Thulu (outside a checkout it writes the deployment files to ~/.octopus):
  {command}' ;;
	'en:install.cli.no_cosign') _i18n_t='  ! cosign not found: only the checksum is verified, not who published the release (install cosign to check the signature)' ;;
	'en:install.cli.not_on_path') _i18n_t='
  ! {dir} is not on your PATH, so `thulu` will not resolve yet. Add it with:
      {command}
    or run it by its full path: {path}' ;;
	'en:install.cli.shadowed') _i18n_t='
  ! {other} comes before {path} on your PATH, so `thulu` runs that one. Remove it, or run {path}' ;;
	'en:install.cli.signature_ok') _i18n_t='  signature ok ({sums} signed by {repo}'\''s release workflow)' ;;
	'en:install.cli.usage') _i18n_t='Installs the thulu CLI from a GitHub release.

  curl -fsSL {url} | sh
  curl -fsSL {url} | sh -s -- --version 1.0.0

Options:
  --version VERSION  install that release instead of the latest (same as OCTOPUS_VERSION)
  -h, --help         show this help

Environment:
  OCTOPUS_INSTALL_DIR=DIR         where the binary goes (default: a writable directory on PATH, else ~/.local/bin)
  OCTOPUS_NO_INIT=1               do not offer to run `thulu init` at the end
  GITHUB_TOKEN=TOKEN              only for a private repository (otherwise downloads come from github.com)
  OCTOPUS_RELEASE_BASE_URL=URL    download from a mirror laid out as <URL>/v<version>/<asset>
  OCTOPUS_REQUIRE_SIGNATURE=1     fail when cosign is not installed
  OCTOPUS_INSECURE_SKIP_VERIFY=1  install when a check cannot run (never when one fails)' ;;
	'en:install.errors.bad_archive') _i18n_t='{asset} has no octopus binary inside' ;;
	'en:install.errors.checksum_mismatch') _i18n_t='checksum mismatch; refusing to install' ;;
	'en:install.errors.dir_not_writable') _i18n_t='you cannot write to {dir}, and this installer does not use sudo. Choose a directory you own, e.g. OCTOPUS_INSTALL_DIR=$HOME/.local/bin' ;;
	'en:install.errors.mirror_needs_version') _i18n_t='OCTOPUS_RELEASE_BASE_URL needs OCTOPUS_VERSION (the mirror cannot answer "latest")' ;;
	'en:install.errors.mkdir_failed') _i18n_t='cannot create {dir}' ;;
	'en:install.errors.no_asset') _i18n_t='release {tag} has no asset named {asset} (or the download failed)' ;;
	'en:install.errors.no_checksum') _i18n_t='release {tag} publishes no checksum for {asset}' ;;
	'en:install.errors.no_checksum_entry') _i18n_t='{sums} has no entry for {asset}; refusing to install' ;;
	'en:install.errors.no_release_id') _i18n_t='the release payload from {repo} carried no id' ;;
	'en:install.errors.no_releases') _i18n_t='cannot find a release in {repo}: none is published yet, or the repository is private.
For a private repository set GITHUB_TOKEN (for example GITHUB_TOKEN=$(gh auth token)).' ;;
	'en:install.errors.no_sha_tool') _i18n_t='no sha256sum or shasum on this machine, so the checksum cannot be checked' ;;
	'en:install.errors.no_signed_checksums') _i18n_t='release {tag} publishes no signed checksums ({sums} + {bundle})' ;;
	'en:install.errors.no_tag_name') _i18n_t='the release payload from {repo} carried no tag_name' ;;
	'en:install.errors.release_not_found') _i18n_t='{repo} has no release {tag}' ;;
	'en:install.errors.required') _i18n_t='{tool} is required and was not found on PATH' ;;
	'en:install.errors.signature_failed') _i18n_t='signature check failed for {sums}: it was not signed by {repo}'\''s release workflow; refusing to install' ;;
	'en:install.errors.signature_required') _i18n_t='OCTOPUS_REQUIRE_SIGNATURE=1 but cosign is not installed' ;;
	'en:install.errors.unsupported_arch') _i18n_t='unsupported architecture {arch}: releases are built for amd64 and arm64' ;;
	'en:install.errors.unsupported_os') _i18n_t='unsupported OS {os}: releases are built for linux and darwin' ;;
	'en:install.errors.unverified_refused') _i18n_t='{reason}; refusing to install (set OCTOPUS_INSECURE_SKIP_VERIFY=1 to install unverified)' ;;
	'en:install.errors.version_needs_value') _i18n_t='--version needs a release, e.g. --version 1.0.0' ;;
	'en:install.errors.write_failed') _i18n_t='cannot write {path} (set OCTOPUS_INSTALL_DIR to a directory you own)' ;;
	'es:common.errors.unknown_argument') _i18n_t='argumento desconocido '\''{arg}'\'' (mirá --help)' ;;
	'es:install.cli.already_installed') _i18n_t='  {tag} ya está instalado en {path}; no hay nada que hacer' ;;
	'es:install.cli.checksum_ok') _i18n_t='  checksum ok' ;;
	'es:install.cli.downloading') _i18n_t='  descargando {asset}' ;;
	'es:install.cli.error') _i18n_t='error: {message}' ;;
	'es:install.cli.header') _i18n_t='Thulu {tag} ({platform})' ;;
	'es:install.cli.init_prompt') _i18n_t='
¿Configurarlo ahora? (thulu init) [S/n]' ;;
	'es:install.cli.installed') _i18n_t='  instalado en {path}' ;;
	'es:install.cli.installing_unverified') _i18n_t='  ! {reason}; se instala igual porque OCTOPUS_INSECURE_SKIP_VERIFY=1' ;;
	'es:install.cli.next_steps') _i18n_t='
Siguiente: configurá y arrancá Thulu (fuera de un checkout escribe los archivos de deploy en ~/.octopus):
  {command}' ;;
	'es:install.cli.no_cosign') _i18n_t='  ! no se encontró cosign: sólo se verifica el checksum, no quién publicó la release (instalá cosign para verificar la firma)' ;;
	'es:install.cli.not_on_path') _i18n_t='
  ! {dir} no está en tu PATH, así que `thulu` todavía no se va a encontrar. Agregalo con:
      {command}
    o correlo con la ruta completa: {path}' ;;
	'es:install.cli.shadowed') _i18n_t='
  ! {other} aparece antes que {path} en tu PATH, así que `thulu` corre ese. Borralo, o corré {path}' ;;
	'es:install.cli.signature_ok') _i18n_t='  firma ok ({sums} firmado por el workflow de release de {repo})' ;;
	'es:install.cli.usage') _i18n_t='Instala el CLI thulu desde una release de GitHub.

  curl -fsSL {url} | sh
  curl -fsSL {url} | sh -s -- --version 1.0.0

Opciones:
  --version VERSIÓN  instala esa release en vez de la última (igual que OCTOPUS_VERSION)
  -h, --help         muestra esta ayuda

Variables de entorno:
  OCTOPUS_INSTALL_DIR=DIR         dónde va el binario (por defecto: un directorio del PATH donde puedas escribir, si no ~/.local/bin)
  OCTOPUS_NO_INIT=1               no ofrece correr `thulu init` al final
  GITHUB_TOKEN=TOKEN              sólo para un repositorio privado (si no, todo se baja de github.com)
  OCTOPUS_RELEASE_BASE_URL=URL    baja de un mirror con la forma <URL>/v<versión>/<asset>
  OCTOPUS_REQUIRE_SIGNATURE=1     falla si cosign no está instalado
  OCTOPUS_INSECURE_SKIP_VERIFY=1  instala aunque un chequeo no se pueda correr (nunca si uno falla)' ;;
	'es:install.errors.bad_archive') _i18n_t='{asset} no trae el binario octopus adentro' ;;
	'es:install.errors.checksum_mismatch') _i18n_t='el checksum no coincide; no se instala' ;;
	'es:install.errors.dir_not_writable') _i18n_t='no tenés permiso de escritura en {dir}, y este instalador no usa sudo. Elegí un directorio tuyo, por ejemplo OCTOPUS_INSTALL_DIR=$HOME/.local/bin' ;;
	'es:install.errors.mirror_needs_version') _i18n_t='OCTOPUS_RELEASE_BASE_URL necesita OCTOPUS_VERSION (el mirror no puede responder "latest")' ;;
	'es:install.errors.mkdir_failed') _i18n_t='no se puede crear {dir}' ;;
	'es:install.errors.no_asset') _i18n_t='la release {tag} no tiene un asset llamado {asset} (o falló la descarga)' ;;
	'es:install.errors.no_checksum') _i18n_t='la release {tag} no publica checksum para {asset}' ;;
	'es:install.errors.no_checksum_entry') _i18n_t='{sums} no tiene una entrada para {asset}; no se instala' ;;
	'es:install.errors.no_release_id') _i18n_t='la release de {repo} no trajo id' ;;
	'es:install.errors.no_releases') _i18n_t='no se encontró ninguna release en {repo}: todavía no se publicó ninguna, o el repositorio es privado.
Para un repositorio privado seteá GITHUB_TOKEN (por ejemplo GITHUB_TOKEN=$(gh auth token)).' ;;
	'es:install.errors.no_sha_tool') _i18n_t='no hay sha256sum ni shasum en esta máquina, así que no se puede verificar el checksum' ;;
	'es:install.errors.no_signed_checksums') _i18n_t='la release {tag} no publica checksums firmados ({sums} + {bundle})' ;;
	'es:install.errors.no_tag_name') _i18n_t='la release de {repo} no trajo tag_name' ;;
	'es:install.errors.release_not_found') _i18n_t='{repo} no tiene la release {tag}' ;;
	'es:install.errors.required') _i18n_t='hace falta {tool} y no está en el PATH' ;;
	'es:install.errors.signature_failed') _i18n_t='falló la verificación de la firma de {sums}: no la firmó el workflow de release de {repo}; no se instala' ;;
	'es:install.errors.signature_required') _i18n_t='OCTOPUS_REQUIRE_SIGNATURE=1 pero cosign no está instalado' ;;
	'es:install.errors.unsupported_arch') _i18n_t='arquitectura {arch} no soportada: las releases se compilan para amd64 y arm64' ;;
	'es:install.errors.unsupported_os') _i18n_t='sistema operativo {os} no soportado: las releases se compilan para linux y darwin' ;;
	'es:install.errors.unverified_refused') _i18n_t='{reason}; no se instala (seteá OCTOPUS_INSECURE_SKIP_VERIFY=1 para instalar sin verificar)' ;;
	'es:install.errors.version_needs_value') _i18n_t='--version necesita una release, por ejemplo --version 1.0.0' ;;
	'es:install.errors.write_failed') _i18n_t='no se puede escribir {path} (seteá OCTOPUS_INSTALL_DIR a un directorio tuyo)' ;;
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

die() {
	msg install.cli.error message "$*" >&2
	exit 1
}

need() { command -v "$1" >/dev/null 2>&1 || die "$(msg install.errors.required tool "$1")"; }

usage() { msg install.cli.usage url "https://github.com/$REPO/releases/latest/download/install.sh"; }

parse_args() {
	while [ $# -gt 0 ]; do
		case $1 in
		-h | --help)
			usage
			exit 0
			;;
		--version)
			if [ $# -lt 2 ] || [ -z "$2" ]; then die "$(msg install.errors.version_needs_value)"; fi
			VERSION=$2
			shift 2
			;;
		--version=*)
			VERSION=${1#*=}
			[ -n "$VERSION" ] || die "$(msg install.errors.version_needs_value)"
			shift
			;;
		*) die "$(msg common.errors.unknown_argument arg "$1")" ;;
		esac
	done
}

detect_platform() {
	_os=$(uname -s | tr '[:upper:]' '[:lower:]')
	_arch=$(uname -m)
	case "$_arch" in
	x86_64 | amd64) _arch=amd64 ;;
	aarch64 | arm64) _arch=arm64 ;;
	*) die "$(msg install.errors.unsupported_arch arch "$_arch")" ;;
	esac
	case "$_os" in
	linux | darwin) ;;
	*) die "$(msg install.errors.unsupported_os os "$_os")" ;;
	esac
	printf '%s_%s' "$_os" "$_arch"
}

on_path() {
	case ":$PATH:" in
	*":$1:"*) return 0 ;;
	*) return 1 ;;
	esac
}

choose_dir() {
	if [ -n "${OCTOPUS_INSTALL_DIR:-}" ]; then
		printf '%s' "$OCTOPUS_INSTALL_DIR"
		return
	fi
	for _dir in /usr/local/bin "$HOME/.local/bin" "$HOME/bin" /opt/homebrew/bin; do
		if on_path "$_dir" && [ -w "$_dir" ]; then
			printf '%s' "$_dir"
			return
		fi
	done
	printf '%s/.local/bin' "$HOME"
}

prepare_dest() {
	DEST=$(choose_dir)
	mkdir -p "$DEST" || die "$(msg install.errors.mkdir_failed dir "$DEST")"
	[ -w "$DEST" ] || die "$(msg install.errors.dir_not_writable dir "$DEST")"
}

already_installed() {
	[ -x "$DEST/$BIN_NAME" ] || return 1
	_current=$("$DEST/$BIN_NAME" version </dev/null 2>/dev/null) || return 1
	[ "$_current" = "${TAG#v}" ]
}

# Only a token the caller names: a piped script must not pick up the gh login.
resolve_token() {
	if [ -n "${GITHUB_TOKEN:-}" ]; then
		printf '%s' "$GITHUB_TOKEN"
		return
	fi
	printf '%s' "${GH_TOKEN:-}"
}

api_get() {
	if [ -n "$TOKEN" ]; then
		curl -fsSL -H "Accept: application/vnd.github+json" -H "Authorization: Bearer $TOKEN" "$1"
	else
		curl -fsSL -H "Accept: application/vnd.github+json" "$1"
	fi
}

release_path() {
	if [ -n "$VERSION" ]; then
		printf 'tags/v%s' "${VERSION#v}"
	else
		printf 'latest' # i18n-ignore the API path segment
	fi
}

# The API pretty-prints JSON (`"tag_name": "v0.1.0"`), hence the whitespace class.
# Release fields precede the free-text body, so the first match is the structural one.
first_number() { grep -oE "\"$1\"[[:space:]]*:[[:space:]]*[0-9]+" | head -n1 | grep -oE "[0-9]+"; }
first_string() { grep -oE "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | head -n1 | cut -d'"' -f4; }

resolve_release() {
	if [ -n "$MIRROR" ]; then
		[ -n "$VERSION" ] || die "$(msg install.errors.mirror_needs_version)"
		TAG="v${VERSION#v}"
		return
	fi
	# Without a token, github.com itself: the API allows 60 anonymous requests an hour per address.
	if [ -z "$TOKEN" ]; then
		if [ -n "$VERSION" ]; then
			TAG="v${VERSION#v}"
			curl -fsSI -o /dev/null "$WEB/$REPO/releases/tag/$TAG" ||
				die "$(msg install.errors.release_not_found repo "$REPO" tag "$TAG")"
			return
		fi
		_latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$WEB/$REPO/releases/latest") ||
			die "$(msg install.errors.no_releases repo "$REPO")"
		case "$_latest" in
		*/releases/tag/?*) TAG=${_latest##*/releases/tag/} ;;
		*) die "$(msg install.errors.no_releases repo "$REPO")" ;;
		esac
		return
	fi
	_release=$(api_get "$API/repos/$REPO/releases/$(release_path)") || die "$(msg install.errors.no_releases repo "$REPO")"
	TAG=$(printf '%s' "$_release" | first_string tag_name) || true
	_id=$(printf '%s' "$_release" | first_number id) || true
	[ -n "$TAG" ] || die "$(msg install.errors.no_tag_name repo "$REPO")"
# Assets come from their own endpoint so JSON-looking changelog text cannot pass for an asset.
	[ -n "$_id" ] || die "$(msg install.errors.no_release_id repo "$REPO")"
	ASSETS_JSON=$(api_get "$API/repos/$REPO/releases/$_id/assets?per_page=100")
}

asset_id() {
	# Each line runs from an asset's `{` to its uploader's `}`; the first id is the asset's, the
	# second the uploader's.
	_name=$(printf '%s' "$1" | sed 's/[].[^$*\\]/\\&/g')
	printf '%s' "$ASSETS_JSON" |
		tr -d '\n\r\t' |
		tr '}' '\n' |
		grep -E "\"name\"[[:space:]]*:[[:space:]]*\"$_name\"" |
		grep -oE "\"id\"[[:space:]]*:[[:space:]]*[0-9]+" |
		head -n1 |
		grep -oE "[0-9]+"
}

fetch() {
	_meter=-s
	if [ "${3:-}" = big ] && [ -t 2 ]; then _meter='-#'; fi
	if [ -n "$MIRROR" ]; then
		# Never send the GitHub token to a mirror host.
		curl -fSL "$_meter" --netrc-optional "$MIRROR/$TAG/$1" -o "$2"
		return
	fi
	if [ -z "$TOKEN" ]; then
		curl -fSL "$_meter" "$WEB/$REPO/releases/download/$TAG/$1" -o "$2"
		return
	fi
	# With a token, the asset API: it also serves a private repository.
	_asset=$(asset_id "$1") || true
	[ -n "$_asset" ] || return 1
	curl -fSL "$_meter" -H "Accept: application/octet-stream" -H "Authorization: Bearer $TOKEN" \
		"$API/repos/$REPO/releases/assets/$_asset" -o "$2"
}

# Only for a check that could not run; a check that ran and failed must die, never come here.
unverified() {
	if [ "$SKIP_VERIFY" = "1" ]; then
		msg install.cli.installing_unverified reason "$1"
		return
	fi
	die "$(msg install.errors.unverified_refused reason "$1")"
}

verify_signature() {
	HAVE_SUMS=""
	if fetch "$SUMS" "$TMP/$SUMS" 2>/dev/null; then
		HAVE_SUMS=1
	fi

	if command -v "$COSIGN" >/dev/null 2>&1; then
		if [ -z "$HAVE_SUMS" ] || ! fetch "$BUNDLE" "$TMP/$BUNDLE" 2>/dev/null; then
			unverified "$(msg install.errors.no_signed_checksums tag "$TAG" sums "$SUMS" bundle "$BUNDLE")"
			return
		fi
		_identity="${OCTOPUS_COSIGN_IDENTITY:-^https://github\.com/$REPO/\.github/workflows/octopus-release\.yml@refs/tags/v}"
		_issuer="${OCTOPUS_COSIGN_ISSUER:-https://token.actions.githubusercontent.com}"
		"$COSIGN" verify-blob "$TMP/$SUMS" \
			--bundle "$TMP/$BUNDLE" \
			--certificate-identity-regexp "$_identity" \
			--certificate-oidc-issuer "$_issuer" >/dev/null 2>&1 ||
			die "$(msg install.errors.signature_failed sums "$SUMS" repo "$REPO")"
		msg install.cli.signature_ok sums "$SUMS" repo "$REPO"
	elif [ "${OCTOPUS_REQUIRE_SIGNATURE:-}" = "1" ]; then
		die "$(msg install.errors.signature_required)"
	else
		msg install.cli.no_cosign
	fi
}

sum_line() {
	awk -v n="$1" '$1 ~ /^[0-9a-f]+$/ && length($1) == 64 && ($2 == n || $2 == "*" n) { print; exit }' "$TMP/$SUMS"
}

choose_asset() {
	ASSET=$BINARY
	if [ -n "$HAVE_SUMS" ] && [ -n "$(sum_line "$BINARY.tar.gz")" ]; then
		need tar
		ASSET=$BINARY.tar.gz
	fi
}

read_checksum() {
	if [ -n "$HAVE_SUMS" ]; then
		sum_line "$ASSET" >"$TMP/$ASSET.sha256"
		[ -s "$TMP/$ASSET.sha256" ] || die "$(msg install.errors.no_checksum_entry sums "$SUMS" asset "$ASSET")"
	elif ! fetch "$ASSET.sha256" "$TMP/$ASSET.sha256" 2>/dev/null; then
		rm -f "$TMP/$ASSET.sha256"
	fi
	if [ ! -s "$TMP/$ASSET.sha256" ]; then
		unverified "$(msg install.errors.no_checksum tag "$TAG" asset "$ASSET")"
	fi
}

verify_checksum() {
	[ -s "$TMP/$ASSET.sha256" ] || return 0 # unverified() already said so
	if command -v sha256sum >/dev/null 2>&1; then
		(cd "$TMP" && sha256sum -c "$ASSET.sha256" >/dev/null) || die "$(msg install.errors.checksum_mismatch)"
	elif command -v shasum >/dev/null 2>&1; then
		(cd "$TMP" && shasum -a 256 -c "$ASSET.sha256" >/dev/null) || die "$(msg install.errors.checksum_mismatch)"
	else
		unverified "$(msg install.errors.no_sha_tool)"
		return
	fi
	msg install.cli.checksum_ok
}

# Extract only the binary, by name, and reject a symlink: nothing else in the archive may land on
# disk.
unpack() {
	case $ASSET in
	*.tar.gz)
		mkdir "$TMP/unpacked"
		tar -xzf "$TMP/$ASSET" -C "$TMP/unpacked" "$ARCHIVE_NAME" || die "$(msg install.errors.bad_archive asset "$ASSET")"
		FILE=$TMP/unpacked/$ARCHIVE_NAME
		if [ ! -f "$FILE" ] || [ -L "$FILE" ]; then die "$(msg install.errors.bad_archive asset "$ASSET")"; fi
		;;
	*) FILE=$TMP/$ASSET ;;
	esac
}

# Stage in DEST and rename: a same-directory rename is atomic, so a running octopus keeps its file
# and nobody runs a half-written one.
install_binary() {
	STAGED="$DEST/.$BIN_NAME.install.$$"
	if ! { cp "$FILE" "$STAGED" && chmod 755 "$STAGED" && mv -f "$STAGED" "$DEST/$BIN_NAME"; }; then
		die "$(msg install.errors.write_failed path "$DEST/$BIN_NAME")"
	fi
	STAGED=""
}

cleanup() {
	if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
	if [ -n "$STAGED" ]; then rm -f "$STAGED"; fi
}

rc_file() {
	case "${SHELL:-}" in
	*/zsh) printf '%s/.zshrc' "$HOME" ;;
	*/bash) printf '%s/.bashrc' "$HOME" ;;
	*) printf '%s/.profile' "$HOME" ;;
	esac
}

path_command() {
	case "${SHELL:-}" in
	*/fish) printf 'fish_add_path %s' "$1" ;;
	*) printf "echo 'export PATH=\"%s:\$PATH\"' >> %s && exec \$SHELL" "$1" "$(rc_file)" ;; # i18n-ignore a shell command
	esac
}

# Under `curl | sh` stdin is the script, so answers must come from /dev/tty.
can_ask() { [ -t 1 ] && (true </dev/tty) 2>/dev/null; }

finish() {
	_bin="$DEST/$BIN_NAME"
	_run="$BIN_NAME init"
	if ! on_path "$DEST"; then
		msg install.cli.not_on_path dir "$DEST" command "$(path_command "$DEST")" path "$_bin"
		_run="$_bin init"
	elif [ "$(command -v "$BIN_NAME")" != "$_bin" ]; then
		msg install.cli.shadowed other "$(command -v "$BIN_NAME")" path "$_bin"
		_run="$_bin init"
	fi

	if [ "${OCTOPUS_NO_INIT:-}" != 1 ] && can_ask; then
		printf '%s ' "$(msg install.cli.init_prompt)"
		read -r _answer </dev/tty || _answer=n
		case $_answer in
		'' | [YySs]*)
			# exec replaces this shell, so the EXIT trap would never run.
			cleanup
			trap - EXIT INT TERM
			exec "$_bin" init </dev/tty
			;;
		esac
	fi
	msg install.cli.next_steps command "$_run"
}

main() {
	REPO="${OCTOPUS_REPO:-MisterDonnuts/thulu}"
	VERSION="${OCTOPUS_VERSION:-}"
	parse_args "$@"

	API="${OCTOPUS_API:-https://api.github.com}"
	WEB="${OCTOPUS_GITHUB_WEB:-https://github.com}"
	WEB="${WEB%/}"
	MIRROR="${OCTOPUS_RELEASE_BASE_URL:-}"
	MIRROR="${MIRROR%/}"
	COSIGN="${OCTOPUS_COSIGN_BIN:-cosign}"
	SKIP_VERIFY="${OCTOPUS_INSECURE_SKIP_VERIFY:-}"
	BIN_NAME="thulu"
	# The release archive still holds the binary as `octopus`.
	ARCHIVE_NAME="octopus"
	TOKEN="" ASSETS_JSON="" TMP="" STAGED=""

	need curl
	PLATFORM=$(detect_platform)
	prepare_dest
	if [ -z "$MIRROR" ]; then
		TOKEN=$(resolve_token)
	fi
	resolve_release
	msg install.cli.header tag "$TAG" platform "$PLATFORM"
	if already_installed; then
		msg install.cli.already_installed tag "$TAG" path "$DEST/$BIN_NAME"
		exit 0
	fi

	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	TMP=$(mktemp -d)

	BINARY="octopus_${TAG#v}_${PLATFORM}"
	SUMS="octopus_${TAG#v}_checksums.txt"
	BUNDLE="$SUMS.sigstore.json"

	verify_signature
	choose_asset
	read_checksum
	msg install.cli.downloading asset "$ASSET"
	fetch "$ASSET" "$TMP/$ASSET" big || die "$(msg install.errors.no_asset tag "$TAG" asset "$ASSET")"
	verify_checksum
	unpack
	install_binary
	msg install.cli.installed path "$DEST/$BIN_NAME"
	finish
}

main "$@"
