package task

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func writeAttachment(ctx context.Context, s *Store, taskID string, n int) error {
	guard, err := s.ReserveAttachment(ctx, taskID)
	if err != nil {
		return err
	}
	defer guard.Done()
	written, err := io.Copy(io.Discard, guard.Limit(bytes.NewReader(make([]byte, n))))
	if err != nil {
		return err
	}
	if err := guard.Check(written); err != nil {
		return err
	}
	_, err = s.AddAttachment(ctx, NewAttachment{TaskID: taskID, Filename: "f.bin", Size: written})
	return err
}

func quotaStore(t *testing.T, quotas map[string]int64) *Store {
	t.Helper()
	s := newTestStore(t)
	s.WithStorageQuota(func(ws string) int64 { return quotas[ws] })
	return s
}

func newTaskIn(t *testing.T, s *Store, ws string) string {
	t.Helper()
	tk := &Task{WorkspaceID: ws, Title: "t"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	return tk.ID
}

func TestStorageQuotaRefusesTheFileThatDoesNotFit(t *testing.T) {
	s := quotaStore(t, map[string]int64{"ws-a": 1000})
	ctx := t.Context()
	id := newTaskIn(t, s, "ws-a")

	if err := writeAttachment(ctx, s, id, 600); err != nil {
		t.Fatalf("first file: %v", err)
	}
	err := writeAttachment(ctx, s, id, 600)
	if !IsStorageQuotaExceeded(err) {
		t.Fatalf("second file err = %v, want storage quota exceeded", err)
	}
	if apperr.Status(err) != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", apperr.Status(err))
	}
	if err := writeAttachment(ctx, s, id, 400); err != nil {
		t.Fatalf("a file that fits exactly: %v", err)
	}
	if err := writeAttachment(ctx, s, id, 1); !IsStorageQuotaExceeded(err) {
		t.Fatalf("full workspace err = %v", err)
	}
	u, err := s.WorkspaceStorage(ctx, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	if u.UsedBytes != 1000 || u.QuotaBytes != 1000 || u.RemainingBytes != 0 {
		t.Errorf("usage = %+v", u)
	}
}

func TestStorageQuotaHoldsUnderParallelUploads(t *testing.T) {
	s := quotaStore(t, map[string]int64{"ws-a": 10_000})
	ctx := t.Context()
	id := newTaskIn(t, s, "ws-a")

	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() { _ = writeAttachment(ctx, s, id, 1000) })
	}
	wg.Wait()
	u, err := s.WorkspaceStorage(ctx, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	if u.UsedBytes != 10_000 {
		t.Errorf("used %d bytes after 40 parallel uploads, want exactly the 10000 quota", u.UsedBytes)
	}
}

func TestStorageQuotaIsPerWorkspace(t *testing.T) {
	s := quotaStore(t, map[string]int64{"ws-a": 500, "ws-b": 500})
	ctx := t.Context()
	a := newTaskIn(t, s, "ws-a")
	b := newTaskIn(t, s, "ws-b")
	free := newTaskIn(t, s, "ws-unlimited")

	if err := writeAttachment(ctx, s, a, 500); err != nil {
		t.Fatal(err)
	}
	if err := writeAttachment(ctx, s, b, 500); err != nil {
		t.Fatalf("ws-b refused because of ws-a's files: %v", err)
	}
	if err := writeAttachment(ctx, s, free, 5000); err != nil {
		t.Fatalf("unlimited workspace refused: %v", err)
	}
	u, err := s.WorkspaceStorage(ctx, "ws-unlimited")
	if err != nil {
		t.Fatal(err)
	}
	if u.UsedBytes != 5000 || u.QuotaBytes != 0 || u.RemainingBytes != -1 {
		t.Errorf("unlimited usage = %+v", u)
	}
}

func TestStorageQuotaWithoutHookIsUnlimited(t *testing.T) {
	s := newTestStore(t)
	id := newTaskIn(t, s, "ws-a")
	if err := writeAttachment(t.Context(), s, id, 4096); err != nil {
		t.Fatal(err)
	}
}

func TestBinaryBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 2 << 30: "2.0 GiB", 1536: "1.5 KiB"} {
		if got := binaryBytes(n); got != want {
			t.Errorf("binaryBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStorageQuotaCoversForkedAttachments(t *testing.T) {
	s := quotaStore(t, map[string]int64{"ws-a": 1000})
	ctx := t.Context()
	src := newTaskIn(t, s, "ws-a")
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, make([]byte, 400), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAttachment(ctx, NewAttachment{TaskID: src, Filename: "f.bin", Path: path, Size: 400}); err != nil {
		t.Fatal(err)
	}

	first := newTaskIn(t, s, "ws-a")
	if err := s.ForkData(ctx, src, first); err != nil {
		t.Fatalf("fork that fits: %v", err)
	}
	second := newTaskIn(t, s, "ws-a")
	err := s.ForkData(ctx, src, second)
	if !IsStorageQuotaExceeded(err) {
		t.Fatalf("fork past the quota err = %v, want storage quota exceeded", err)
	}
	atts, err := s.ListAttachments(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 0 {
		t.Errorf("refused fork still got %d attachment rows", len(atts))
	}
	u, err := s.WorkspaceStorage(ctx, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	if u.UsedBytes != 800 {
		t.Errorf("used %d bytes after a refused fork, want 800", u.UsedBytes)
	}
}
