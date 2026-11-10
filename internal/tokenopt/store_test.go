package tokenopt_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
)

func TestMigrateDropsTheSettingsTable(t *testing.T) {
	db := testpg.New(t)
	for range 2 {
		if err := tokenopt.Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	var table *string
	if err := db.QueryRowContext(t.Context(), `SELECT to_regclass('token_optimizer_settings')::text`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != nil {
		t.Fatalf("token_optimizer_settings survived: %s", *table)
	}
}
