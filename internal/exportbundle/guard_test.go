package exportbundle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/exportbundle"
)

func TestImportRefusesUnknownVersion(t *testing.T) {
	b := &exportbundle.Bundle{Version: exportbundle.Version + 99}
	_, err := exportbundle.Import(context.Background(), exportbundle.Stores{}, b, nil, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("want a version-refusal error, got %v", err)
	}
}

func TestReadRejectsNonGzip(t *testing.T) {
	if _, _, err := exportbundle.Read(strings.NewReader("not a gzip")); err == nil {
		t.Fatal("want an error reading a non-gzip body")
	}
}
