#!/bin/sh
# Regenerates THIRD_PARTY_NOTICES: the licenses of the third-party code that the
# octopus binary contains, which is what the release tarballs and the daemon image
# ship.
#
#   scripts/third-party-notices.sh            # writes THIRD_PARTY_NOTICES at the repo root
#   scripts/third-party-notices.sh -          # writes to stdout
#   scripts/third-party-notices.sh some/path  # writes there
#
# It does not touch the network. It needs:
#   - the Go module cache filled (`go mod download`, once);
#   - web/node_modules installed (`npm ci` in web/, which `make build-web` already needs);
#   - node, to read web/package.json and web/package-lock.json.
#
# What it covers:
#   - Go: every module linked into the binary for the targets the release builds: the
#     daemon (default tags, linux amd64/arm64, as in deploy/Dockerfile) and the host CLI
#     (-tags hostcli, linux and darwin, amd64/arm64, as in octopus-release.yml). Both
#     builds include ee/. A `-tags foss` build links a subset of the same modules.
#   - The Go standard library and runtime, from the toolchain that runs this script.
#   - npm: the "dependencies" of web/package.json, the only npm code that ends up in
#     web/dist (embedded by web/embed.go). Their own dependencies and every
#     devDependency run at build time and do not ship. A built web/dist
#     holds the Astro client router, DOMPurify and the font
#     files.
#
# Exit status: 1 if a module has no license file or a license this script does not
# recognise, or if a license looks copyleft; the file is still written, for review.

set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

OUT=${1:-$ROOT/THIRD_PARTY_NOTICES}

export GOPROXY=off GOFLAGS=-mod=readonly CGO_ENABLED=0 GOWORK=off

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

status=0
warn() {
	printf 'third-party-notices: %s\n' "$*" >&2
	status=1
}

# Fails here, with a clear message, if a module is missing from the cache: with
# GOPROXY=off `go list -e` below would otherwise drop it without saying so.
if ! go mod download >/dev/null 2>"$tmp/download.err"; then
	cat "$tmp/download.err" >&2
	echo "third-party-notices: the Go module cache is incomplete; run 'go mod download' once (needs network)" >&2
	exit 2
fi

# OS ARCH TAGS ("-" for none)
for target in \
	"linux amd64 -" "linux arm64 -" \
	"linux amd64 hostcli" "linux arm64 hostcli" \
	"darwin amd64 hostcli" "darwin arm64 hostcli"; do
	# shellcheck disable=SC2086 # word splitting is the point
	set -- $target
	tags=
	[ "$3" = "-" ] || tags=$3
	GOOS=$1 GOARCH=$2 go list -e -deps -tags "$tags" \
		-f '{{if .Error}}ERROR	{{.ImportPath}}	{{.Error}}{{"\n"}}{{end}}{{with .Module}}{{if not .Main}}{{.Path}}	{{.Version}}	{{.Dir}}{{"\n"}}{{end}}{{end}}' \
		. >>"$tmp/list"
done

# web/dist is built separately; its absence is the one error that does not matter here.
if grep '^ERROR	' "$tmp/list" | grep -v 'pattern all:dist: no matching files found' >"$tmp/errors"; then
	cat "$tmp/errors" >&2
	warn "go list reported errors"
fi
grep -v '^ERROR	' "$tmp/list" | grep -v '^$' | sort -u >"$tmp/mods"

# Prints the SPDX id the text of $1 looks like. A guess for the summary table only:
# the full texts below are what count.
classify() {
	if grep -q 'Apache License' "$1" && grep -q 'Version 2.0' "$1"; then
		echo Apache-2.0
	elif grep -q 'Mozilla Public License' "$1"; then
		echo MPL-2.0
	elif grep -qE 'GNU (Affero |Lesser |Library )?General Public License' "$1"; then
		echo GPL-FAMILY
	elif grep -q 'SIL OPEN FONT LICENSE' "$1"; then
		echo OFL-1.1
	elif grep -q 'Permission is hereby granted, free of charge' "$1"; then
		echo MIT
	elif grep -qE 'Permission to use, copy, modify, and(/or)? distribute this software for any' "$1"; then
		echo ISC
	elif grep -q 'Redistribution and use in source and binary forms' "$1"; then
		if grep -qiE 'neither the name|names of (its|the) contributors|name of the copyright holder' "$1"; then
			echo BSD-3-Clause
		else
			echo BSD-2-Clause
		fi
	else
		echo UNKNOWN
	fi
}

# Lists, one per line, the license-like files at the top of directory $1: LICENSE*
# first, then COPYING*, NOTICE*, PATENTS*.
license_files() {
	for pat in '[Ll][Ii][Cc][Ee][Nn][CcSs][Ee]*' '[Cc][Oo][Pp][Yy][Ii][Nn][Gg]*' \
		'[Nn][Oo][Tt][Ii][Cc][Ee]*' '[Pp][Aa][Tt][Ee][Nn][Tt][Ss]*'; do
		for f in "$1"/$pat; do
			# LICENSE.docs and the like cover documentation, which the binary does not carry.
			case $f in *[.-][Dd][Oo][Cc][Ss]*) continue ;; esac
			[ -f "$f" ] && printf '%s\n' "$f"
		done
	done | awk '!seen[$0]++'
}

# Prints a checksum of $1 that ignores whitespace, to spot identical license texts.
textsum() {
	tr -d '\r' <"$1" | tr -s '[:space:]' ' ' | sed 's/^ //; s/ $//' | cksum | cut -d' ' -f1
}

# Prints $1 without carriage returns or trailing blank lines, so the output is stable.
print_text() {
	tr -d '\r' <"$1" | awk '{ lines[NR] = $0 } END { n = NR; while (n > 0 && lines[n] ~ /^[ \t]*$/) n--; for (i = 1; i <= n; i++) print lines[i] }'
}

# Writes one entry: $1 name, $2 version, $3 directory, $4 (optional) the SPDX id to
# keep when the component is offered under a choice of licenses. Appends a summary row.
entry() {
	files=$(license_files "$3")
	if [ -z "$files" ]; then
		warn "$1 $2: no license file in $3"
		printf '%s\t%s\t%s\n' "$1" "$2" "NOT FOUND" >>"$tmp/summary"
		return
	fi
	elect=${4:-}
	kinds=
	kept=
	# Paths are split on newlines only, so a directory with spaces still works.
	oldifs=$IFS
	IFS='
'
	for f in $files; do
		case $(basename "$f") in
		[Nn][Oo][Tt][Ii][Cc][Ee]* | [Pp][Aa][Tt][Ee][Nn][Tt][Ss]*)
			kept="$kept$f
"
			continue
			;;
		esac
		k=$(classify "$f")
		if [ -n "$elect" ] && [ "$k" != "$elect" ]; then
			continue
		fi
		kept="$kept$f
"
		case " $kinds " in *" $k "*) ;; *) kinds="${kinds:+$kinds }$k" ;; esac
	done
	case " $kinds " in
	*" UNKNOWN "*) warn "$1 $2: license not recognised, review by hand" ;;
	*" GPL-FAMILY "* | *" MPL-2.0 "*) warn "$1 $2: copyleft license ($kinds), review by hand" ;;
	esac
	printf '%s\t%s\t%s\n' "$1" "$2" "$kinds" >>"$tmp/summary"
	{
		echo '================================================================================'
		printf '%s %s\n' "$1" "$2"
		printf 'License: %s\n' "$kinds"
		IFS='
'
		for f in $kept; do
			echo '--------------------------------------------------------------------------------'
			printf '%s\n\n' "$(basename "$f")"
			if [ -n "$apache_sum" ] && [ "$(textsum "$f")" = "$apache_sum" ]; then
				echo 'The standard text of the Apache License, Version 2.0, reproduced once'
				echo 'under "APACHE LICENSE 2.0" at the end of this file.'
			else
				print_text "$f"
			fi
		done
		IFS=$oldifs
		echo
	} >>"$tmp/body"
}

# --- npm packages embedded in web/dist -------------------------------------------
if ! command -v node >/dev/null 2>&1; then
	echo "third-party-notices: node is required to read web/package-lock.json" >&2
	exit 2
fi
if [ ! -d web/node_modules ]; then
	echo "third-party-notices: web/node_modules is missing; run 'npm ci' in web/" >&2
	exit 2
fi
node -e '
const fs = require("fs");
const pkg = JSON.parse(fs.readFileSync("web/package.json", "utf8"));
const lock = JSON.parse(fs.readFileSync("web/package-lock.json", "utf8"));
for (const name of Object.keys(pkg.dependencies || {}).sort()) {
  const e = (lock.packages || {})["node_modules/" + name];
  if (!e) { console.error("third-party-notices: " + name + " is not in package-lock.json"); process.exit(3); }
  console.log([name, e.version, e.license || ""].join("\t"));
}' >"$tmp/npm"

goroot=$(go env GOROOT)

# The Apache-2.0 text most modules carry unchanged is printed once, at the end. Any
# file that differs from it (a filled-in copyright line, an extra paragraph) is
# printed in full where it belongs.
{
	printf '%s\n' "$goroot"
	cut -f3 "$tmp/mods"
	cut -f1 "$tmp/npm" | sed 's|^|web/node_modules/|'
} | while IFS= read -r d; do
	license_files "$d"
done | while IFS= read -r f; do
	[ "$(classify "$f")" = Apache-2.0 ] && printf '%s\t%s\n' "$(textsum "$f")" "$f"
done | sort >"$tmp/apache"
apache_sum=$(cut -f1 "$tmp/apache" | uniq -c | sort -k1,1nr -k2,2 | awk 'NR == 1 { print $2 }')
apache_file=$(awk -F'\t' -v s="$apache_sum" '$1 == s { print $2; exit }' "$tmp/apache")

: >"$tmp/summary"
: >"$tmp/body"

# --- Go standard library and runtime ---------------------------------------------
entry "Go standard library and runtime" "$(go env GOVERSION)" "$goroot"

# --- Go modules ----------------------------------------------------------------------
while IFS='	' read -r path version dir; do
	if [ -z "$dir" ]; then
		warn "$path $version: not in the module cache"
		continue
	fi
	entry "$path" "$version" "$dir"
done <"$tmp/mods"

# --- npm ---------------------------------------------------------------------------
while IFS='	' read -r name version declared; do
	dir=web/node_modules/$name
	installed=$(node -p "require('./$dir/package.json').version" 2>/dev/null || echo missing)
	if [ "$installed" != "$version" ]; then
		warn "$name: package-lock.json has $version, web/node_modules has $installed; run 'npm ci' in web/"
	fi
	# A package offered under a choice that includes Apache-2.0 (DOMPurify:
	# "(MPL-2.0 OR Apache-2.0)") is taken under Apache-2.0.
	elect=
	case $declared in *" OR "*Apache-2.0* | *Apache-2.0*" OR "*) elect=Apache-2.0 ;; esac
	entry "npm:$name" "$version" "$dir" "$elect"
	# The copyright banner of the shipped file, for packages whose license file has none.
	banner=$(grep -rhoE -m1 '@license [^*]*' "$dir/dist" 2>/dev/null | head -n 1 | sed 's/[[:space:]]*$//' || true)
	{
		printf 'Declared in package.json: %s\n' "$declared"
		if [ -n "$banner" ]; then printf 'Banner in the shipped file: %s\n' "$banner"; fi
		if [ -n "$elect" ]; then printf 'Thulu uses this package under: %s\n' "$elect"; fi
		echo
	} >>"$tmp/body"
done <"$tmp/npm"

# --- assemble ------------------------------------------------------------------------
{
	cat <<'HEADER'
THIRD-PARTY SOFTWARE NOTICES

The thulu / octopus binary, the release archives and the container images that
carry it include the third-party software listed below. Each component is
licensed by its authors under the license reproduced here; nothing in Thulu's
own licenses (LICENSE, LICENSE-EXCEPTION, ee/LICENSE) changes those terms.

This file covers the Go modules linked into the binary, the Go standard library
and runtime, and the npm packages embedded in the web dashboard (web/dist).
It does not cover the operating system packages or other programs inside the
container images; those keep the notices of their own distributions.

Generated by scripts/third-party-notices.sh from go.mod, go.sum and
web/package-lock.json. Do not edit by hand.

SUMMARY

HEADER
	awk -F'\t' '{ printf "  %-62s %-38s %s\n", $1, $2, $3 }' "$tmp/summary"
	echo
	cat "$tmp/body"
	if [ -n "$apache_file" ]; then
		echo '================================================================================'
		echo 'APACHE LICENSE 2.0'
		echo '--------------------------------------------------------------------------------'
		echo
		print_text "$apache_file"
	fi
} >"$tmp/out"

if [ "$OUT" = "-" ]; then
	cat "$tmp/out"
else
	mv "$tmp/out" "$OUT"
fi

exit "$status"
