#!/usr/bin/env bash
set -euo pipefail

GO=${GO:-go}
API_DIR=internal/api
API_SHARDS=2
MODULE=github.com/MisterDonnuts/thulu
TEST_FLAGS=(-race -timeout 1200s -covermode=atomic)
# -parallel 4 keeps the per-test pools (10 connections each) under Postgres's max_connections of
# 100.
API_PARALLEL=(-parallel 4)

die() {
	echo "go-test-shard: $*" >&2
	exit 1
}

shards() {
	local i
	for ((i = 1; i <= API_SHARDS; i++)); do echo "api-$i"; done
	echo rest
}

# Mirrors go test's top-level name rule (char after Test/Example/Fuzz must not be lowercase). -u:
# internal and external test packages may share a name.
api_tests() {
	local files=("$API_DIR"/*_test.go)
	[ -e "${files[0]}" ] || die "no test files in $API_DIR"
	grep -h -E '^func (Test|Example|Fuzz)' "${files[@]}" |
		sed -E -n 's/^func ((Test|Example|Fuzz)([^a-z(][A-Za-z0-9_]*)?)\(.*/\1/p' |
		grep -v -x 'TestMain' |
		LC_ALL=C sort -u
}

api_shard_tests() {
	api_tests | awk -v n="$API_SHARDS" -v k="$1" '(NR - 1) % n == k - 1'
}

api_pattern() {
	local names
	names=$(api_shard_tests "$1" | paste -s -d '|' -)
	[ -n "$names" ] || die "api shard $1 has no tests"
	printf '^(%s)$' "$names"
}

rest_packages() {
	local pkgs
	pkgs=$("$GO" list -e ./...)
	echo "$pkgs" | grep -q -x "$MODULE/$API_DIR" || die "$MODULE/$API_DIR is not in 'go list ./...'; update API_DIR"
	echo "$pkgs" | grep -v -x "$MODULE/$API_DIR"
}

check() {
	local all total=0 n count matched selected
	all=$(api_tests)
	[ -n "$all" ] || die "found no tests in $API_DIR"
	selected=""
	for ((n = 1; n <= API_SHARDS; n++)); do
		count=$(api_shard_tests "$n" | grep -c .) || die "api shard $n is empty"
		matched=$(echo "$all" | grep -E -x "$(api_pattern "$n")" || true)
		[ "$matched" = "$(api_shard_tests "$n")" ] ||
			die "api shard $n: its -run pattern does not select exactly its own tests"
		selected+="$matched"$'\n'
		total=$((total + count))
	done
	local dupes missing
	dupes=$(printf '%s' "$selected" | LC_ALL=C sort | uniq -d)
	[ -z "$dupes" ] || die "tests in more than one api shard: $(echo "$dupes" | paste -s -d ' ' -)"
	missing=$(LC_ALL=C comm -23 <(echo "$all") <(printf '%s' "$selected" | LC_ALL=C sort))
	[ -z "$missing" ] || die "tests in no api shard: $(echo "$missing" | paste -s -d ' ' -)"
	[ "$total" -eq "$(echo "$all" | grep -c .)" ] || die "shard sizes do not add up to the test count"
	echo "go-test-shard: $total tests in $API_DIR over $API_SHARDS shards, each exactly once"
}

list() {
	local n
	case "$1" in
	api-*)
		n=$(shard_index "$1")
		api_shard_tests "$n"
		;;
	rest) rest_packages ;;
	*) die "unknown shard $1 (want one of: $(shards | paste -s -d ' ' -))" ;;
	esac
}

shard_index() {
	local n=${1#api-}
	case "$n" in '' | *[!0-9]*) die "unknown shard $1" ;; esac
	[ "$n" -ge 1 ] && [ "$n" -le "$API_SHARDS" ] || die "unknown shard $1 (there are $API_SHARDS api shards)"
	echo "$n"
}

run() {
	local shard=$1 out="coverage-$1.out"
	check >/dev/null
	case "$shard" in
	api-*)
		local n pattern
		n=$(shard_index "$shard")
		pattern=$(api_pattern "$n")
		echo "==> go test ${TEST_FLAGS[*]} ${API_PARALLEL[*]} -coverprofile=$out ./$API_DIR ($shard: $(api_shard_tests "$n" | grep -c .) tests)"
		"$GO" test "${TEST_FLAGS[@]}" "${API_PARALLEL[@]}" -coverprofile="$out" -run "$pattern" "./$API_DIR"
		;;
	rest)
		local pkgs
		pkgs=$(rest_packages)
		echo "==> go test ${TEST_FLAGS[*]} -coverprofile=$out <every package but $API_DIR: $(echo "$pkgs" | grep -c .)>"
		# shellcheck disable=SC2086
		"$GO" test "${TEST_FLAGS[@]}" -coverprofile="$out" $pkgs
		;;
	*) die "unknown shard $shard (want one of: $(shards | paste -s -d ' ' -))" ;;
	esac
}

# cover's parser rejects a second mode line and sums duplicate blocks, so merge = one mode line +
# every body.
# A missing shard profile fails here: raising API_SHARDS needs the shard added to the workflow
# matrix.
merge() {
	local out=coverage.out s f
	while IFS= read -r s; do
		f="coverage-$s.out"
		[ -s "$f" ] || die "merge: $f is missing or empty (did shard $s run?)"
		[ "$(head -n 1 "$f")" = "mode: atomic" ] || die "merge: $f is not an atomic profile"
	done < <(shards)
	{
		echo "mode: atomic"
		while IFS= read -r s; do tail -n +2 "coverage-$s.out"; done < <(shards)
	} >"$out.tmp"
	mv "$out.tmp" "$out"
	echo "go-test-shard: merged $(shards | grep -c .) profiles into $out"
}

all() {
	check
	local names=() pids=() failed=() s i status logdir
	while IFS= read -r s; do names+=("$s"); done < <(shards)
	for s in "${names[@]}"; do rm -f "coverage-$s.out"; done
	if [ "${GO_TEST_SEQUENTIAL:-}" = 1 ]; then
		for s in "${names[@]}"; do run "$s" || failed+=("$s"); done
	else
		logdir=$(mktemp -d)
		for s in "${names[@]}"; do
			run "$s" >"$logdir/$s.log" 2>&1 &
			pids+=($!)
		done
		for i in "${!names[@]}"; do
			s=${names[$i]}
			if wait "${pids[$i]}"; then status=ok; else status=FAILED; failed+=("$s"); fi
			echo "──── shard $s: $status ────"
			cat "$logdir/$s.log"
		done
		rm -rf "$logdir"
	fi
	[ "${#failed[@]}" -eq 0 ] || die "failed shards: ${failed[*]}"
	merge
}

cmd=${1:-}
[ "$#" -gt 0 ] && shift
case "$cmd" in
shards) shards ;;
check) check ;;
list)
	[ "$#" -eq 1 ] || die "usage: $0 list <shard>"
	list "$1"
	;;
run)
	[ "$#" -eq 1 ] || die "usage: $0 run <shard>"
	run "$1"
	;;
all) all ;;
merge) merge ;;
*) die "usage: $0 {shards|check|list <shard>|run <shard>|all|merge}" ;;
esac
