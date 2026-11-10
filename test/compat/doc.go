// Package compat is the API smoke suite behind the compat gate
// (docs/migrations.md): the previous release and the current commit take turns
// on one database, and each must read what the other wrote and keep writing.
//
// The tests carry the build tag `compat` and talk HTTP to a daemon that
// scripts/compat.sh starts; without the tag this package is empty, so
// `go test ./...` and the linters never see them. Run the whole gate with
// `make compat`, or one step by hand:
//
//	COMPAT_URL=http://127.0.0.1:18585 COMPAT_DEV_TOKEN=... COMPAT_API_KEY_FILE=... \
//	COMPAT_STATE=/tmp/state.json go test -tags compat ./test/compat -run TestSeed
package compat
