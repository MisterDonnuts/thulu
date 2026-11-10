package editiontest

import (
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/edition"
)

type license []edition.Feature

func (l license) Has(f edition.Feature) bool { return len(l) == 0 || slices.Contains(l, f) }

func (l license) Status() edition.Status {
	s := edition.Status{Edition: edition.Enterprise, Licensee: "test", Features: []string{}}
	for _, f := range l {
		s.Features = append(s.Features, string(f))
	}
	return s
}

// Enable installs a license for the rest of the test: the given features, or every feature when
// none is named.
func Enable(t testing.TB, features ...edition.Feature) {
	t.Helper()
	swap(t, license(features))
}

// Community removes any license for the rest of the test.
func Community(t testing.TB) {
	t.Helper()
	swap(t, nil)
}

// EnableAll installs a license with every feature for a whole test binary, from TestMain.
func EnableAll() { edition.SetLicense(license(nil)) }

func swap(t testing.TB, l edition.License) {
	prev := edition.Installed()
	edition.SetLicense(l)
	t.Cleanup(func() { edition.SetLicense(prev) })
}
