package task

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

// This package cannot use internal/schema/schematest (it imports this one), so the template is the
// task store's alone.
func newTaskDB(t *testing.T) *sql.DB {
	t.Helper()
	key, err := taskTemplateKey()
	if err != nil {
		t.Fatalf("task template key: %v", err)
	}
	return testpg.NewFromTemplate(t, testpg.Template{Name: "task", Key: key, Build: buildTaskTemplate})
}

var taskTemplateKey = sync.OnceValues(func() (string, error) {
	list, err := Migrations(migrate.Everything)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintln(h, "builder 1")
	for _, stmt := range taskSchema {
		fmt.Fprintf(h, "base %d\n%s\n", len(stmt), stmt)
	}
	for _, m := range list {
		fmt.Fprintf(h, "migration %d %d\n%s\n", m.ID, len(m.SQL), m.SQL)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
})

// Opens the store twice, as two boots would: a guarded migration that qualifies only after an
// earlier one ran lands in the template.
func buildTaskTemplate(_ context.Context, db *sql.DB) error {
	for range 2 {
		if _, err := NewStore(db, ""); err != nil {
			return err
		}
	}
	return nil
}
