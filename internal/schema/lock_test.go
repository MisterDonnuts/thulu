package schema

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/migrate"
)

func entry(component string, id int, sql string) Entry {
	sum := sha256.Sum256([]byte(sql))
	return Entry{Component: component, ID: id, SQL: sql, SHA256: hex.EncodeToString(sum[:])}
}

func lockOf(t *testing.T, releases []string, all []Entry, released map[string]string) *Lock {
	t.Helper()
	l := &Lock{Releases: releases}
	for _, e := range all {
		l.Entries = append(l.Entries, LockEntry{
			Component: e.Component, ID: e.ID, SHA256: e.SHA256,
			ReleasedIn: released[entryKey(e.Component, e.ID)],
		})
	}
	return l
}

func wantProblem(t *testing.T, p Problems, substr string) {
	t.Helper()
	for _, s := range p {
		if strings.Contains(s, substr) {
			return
		}
	}
	t.Errorf("no problem mentions %q; got %q", substr, p)
}

func TestLockRoundTrip(t *testing.T) {
	all := []Entry{entry("b", 1, "x"), entry("a", 2, "y"), entry("a", 1, "z")}
	l := lockOf(t, []string{"0.1.0", "0.2.0"}, all, map[string]string{"a/1": "0.1.0"})
	out := l.Format()
	back, err := ParseLock(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Format(), out) {
		t.Errorf("format is not stable:\n%s\nvs\n%s", out, back.Format())
	}
	if back.Entries[0].Key() != "a/1" || back.Entries[0].ReleasedIn != "0.1.0" || back.Entries[1].ReleasedIn != "" {
		t.Errorf("entries = %+v", back.Entries)
	}
	if back.Latest() != "0.2.0" {
		t.Errorf("latest = %q", back.Latest())
	}
}

func TestParseLockRejects(t *testing.T) {
	h := strings.Repeat("a", 64)
	for name, body := range map[string]string{
		"duplicate":         "release 0.1.0\nx 1 " + h + " -\nx 1 " + h + " -\n",
		"unknown release":   "x 1 " + h + " 0.1.0\n",
		"short hash":        "x 1 abc -\n",
		"bad id":            "x 0 " + h + " -\n",
		"v-prefixed":        "release v0.1.0\n",
		"build metadata":    "release 0.1.0+abc\n",
		"missing field":     "x 1 " + h + "\n",
		"not a version":     "release latest\n",
		"release extra arg": "release 0.1.0 now\n",
	} {
		if _, err := ParseLock(strings.NewReader(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheck(t *testing.T) {
	all := []Entry{entry("a", 1, "create table a"), entry("a", 2, "alter table a add column b text")}
	l := lockOf(t, []string{"0.1.0"}, all, map[string]string{"a/1": "0.1.0"})
	if p := Check(l, all); len(p) != 0 {
		t.Fatalf("clean lock: %v", p)
	}

	edited := []Entry{entry("a", 1, "create table a -- tweak"), all[1]}
	wantProblem(t, Check(l, edited), "a/1 shipped in 0.1.0 and its SQL changed")

	wantProblem(t, Check(l, []Entry{all[0], entry("a", 2, "alter table a add column c text")}), "run `make migrations-lock`")

	wantProblem(t, Check(l, append(all, entry("a", 3, "select 1"))), "a/3 is not in the lock")

	wantProblem(t, Check(l, all[1:]), "a/1 shipped in 0.1.0 and is gone")

	bad := all[1]
	bad.Contract = &migrate.Contract{StoppedUsingIn: "soon"}
	p := Check(l, []Entry{all[0], bad})
	wantProblem(t, p, "StoppedUsingIn")
	wantProblem(t, p, "Reason is empty")
}

func TestCheckAgainstBase(t *testing.T) {
	all := []Entry{entry("a", 1, "one"), entry("a", 2, "two")}
	base := lockOf(t, []string{"0.1.0"}, all, map[string]string{"a/1": "0.1.0"})

	next := lockOf(t, []string{"0.1.0", "0.2.0"}, append(all, entry("a", 3, "three")),
		map[string]string{"a/1": "0.1.0", "a/2": "0.2.0"})
	if p := CheckAgainstBase(next, base); len(p) != 0 {
		t.Fatalf("legit change refused: %v", p)
	}

	hidden := lockOf(t, []string{"0.1.0"}, []Entry{entry("a", 1, "one, edited"), all[1]}, map[string]string{"a/1": "0.1.0"})
	wantProblem(t, CheckAgainstBase(hidden, base), "a/1 shipped in 0.1.0 and its hash changed")

	wantProblem(t, CheckAgainstBase(lockOf(t, []string{"0.1.0"}, all[1:], nil), base), "a/1 shipped in 0.1.0 and was removed")

	wantProblem(t, CheckAgainstBase(lockOf(t, []string{"0.1.0"}, all, nil), base), `the lock now says ""`)

	wantProblem(t, CheckAgainstBase(lockOf(t, nil, all, nil), base), "release 0.1.0 was removed")

	wantProblem(t, CheckAgainstBase(lockOf(t, []string{"0.1.0", "0.0.9"}, all, map[string]string{"a/1": "0.1.0"}), base),
		"release 0.0.9 added")

	moved := lockOf(t, []string{"0.1.0"}, []Entry{all[0], entry("a", 2, "two, reworked")}, map[string]string{"a/1": "0.1.0"})
	if p := CheckAgainstBase(moved, base); len(p) != 0 {
		t.Errorf("unreleased edit refused: %v", p)
	}
}

func TestRelock(t *testing.T) {
	all := []Entry{entry("a", 1, "one"), entry("a", 2, "two")}
	l := lockOf(t, []string{"0.1.0"}, all, map[string]string{"a/1": "0.1.0"})

	next, err := Relock(l, []Entry{all[0], entry("a", 2, "two, edited"), entry("b", 1, "new")})
	if err != nil {
		t.Fatal(err)
	}
	if p := Check(next, []Entry{all[0], entry("a", 2, "two, edited"), entry("b", 1, "new")}); len(p) != 0 {
		t.Errorf("relocked lock does not check: %v", p)
	}
	if next.Entries[0].ReleasedIn != "0.1.0" {
		t.Errorf("relock lost released_in: %+v", next.Entries[0])
	}

	if _, err := Relock(l, []Entry{entry("a", 1, "one, edited"), all[1]}); err == nil {
		t.Error("relock rewrote a published hash")
	}
	if _, err := Relock(l, all[1:]); err == nil {
		t.Error("relock dropped a published migration")
	}
}

func TestMarkReleasedAndReady(t *testing.T) {
	all := []Entry{entry("a", 1, "one"), entry("a", 2, "two")}
	l := lockOf(t, []string{"0.1.0"}, all, map[string]string{"a/1": "0.1.0"})

	wantProblem(t, CheckReady(l, "0.2.0"), "does not list release 0.2.0")
	wantProblem(t, CheckReady(l, "0.2.0"), "a/2 is not marked as shipped")

	for _, v := range []string{"0.1.0", "0.0.5", "v0.2.0", "0.2"} {
		if _, err := MarkReleased(l, v); err == nil {
			t.Errorf("MarkReleased(%q) accepted", v)
		}
	}
	next, err := MarkReleased(l, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if next.Entries[0].ReleasedIn != "0.1.0" || next.Entries[1].ReleasedIn != "0.2.0" {
		t.Errorf("entries = %+v", next.Entries)
	}
	if p := CheckReady(next, "0.2.0"); len(p) != 0 {
		t.Errorf("ready after release: %v", p)
	}
	if p := CheckAgainstBase(next, l); len(p) != 0 {
		t.Errorf("a release PR fails the base check: %v", p)
	}
}

func TestCheckedInLock(t *testing.T) {
	raw, err := os.ReadFile("migrations.lock")
	if err != nil {
		t.Fatal(err)
	}
	l, err := ParseLock(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, l.Format()) {
		t.Error("migrations.lock is not in canonical form: run `make migrations-lock`")
	}
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(l, all).Err(); err != nil {
		t.Error(err)
	}
}
