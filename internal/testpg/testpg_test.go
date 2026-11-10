package testpg_test

import (
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestNew_ProvisionsIsolatedDB(t *testing.T) {
	db := testpg.New(t)

	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	if !strings.HasPrefix(name, "octopus_test_") {
		t.Errorf("unexpected database name %q (want octopus_test_*)", name)
	}

	if _, err := db.Exec(`CREATE TABLE leakcheck (x int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func TestNew_TwoCallsGiveDifferentDBs(t *testing.T) {
	a := testpg.New(t)
	b := testpg.New(t)
	var na, nb string
	if err := a.QueryRow(`SELECT current_database()`).Scan(&na); err != nil {
		t.Fatalf("a current_database: %v", err)
	}
	if err := b.QueryRow(`SELECT current_database()`).Scan(&nb); err != nil {
		t.Fatalf("b current_database: %v", err)
	}
	if na == nb {
		t.Errorf("both calls returned the same database %q — testpg.New must hand out unique DBs", na)
	}
}
