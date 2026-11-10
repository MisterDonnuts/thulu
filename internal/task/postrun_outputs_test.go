package task

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestPostRunReadsTheRunOutputDir(t *testing.T) {
	cases := []struct {
		name  string
		stage Stage
		files map[string]string
		check func(t *testing.T, store *Store, taskID, instanceID string)
	}{
		{
			name:  "stage run posts _index.md as the agent report",
			stage: types.StageInProgress,
			files: map[string]string{"_index.md": "## Plan\nthe detail"},
			check: func(t *testing.T, store *Store, taskID, instanceID string) {
				reports, err := store.ListReports(t.Context(), taskID)
				if err != nil {
					t.Fatalf("ListReports: %v", err)
				}
				for _, r := range reports {
					if r.Kind == ReportKindAgentIndex && r.Body == "## Plan\nthe detail" && r.InstanceID == instanceID {
						return
					}
				}
				t.Fatalf("reports = %+v, want the agent's _index.md for instance %s", reports, instanceID)
			},
		},
		{
			name:  "stage run skips an _index.md the entrypoint already filed",
			stage: types.StageInProgress,
			files: map[string]string{"_index.md": "## Plan", "_index.posted": ""},
			check: func(t *testing.T, store *Store, taskID, _ string) {
				reports, err := store.ListReports(t.Context(), taskID)
				if err != nil {
					t.Fatalf("ListReports: %v", err)
				}
				for _, r := range reports {
					if r.Kind == ReportKindAgentIndex {
						t.Fatalf("reports = %+v, want no second copy of the run index", reports)
					}
				}
			},
		},
		{
			name:  "stage run keeps the session to resume",
			stage: types.StageInProgress,
			files: map[string]string{".session_id": "sess-7\n"},
			check: func(t *testing.T, store *Store, taskID, _ string) {
				got, err := store.Get(t.Context(), taskID)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got.SessionID != "sess-7" || got.SessionAgent != "developer" {
					t.Fatalf("session = %q by %q, want sess-7 by developer", got.SessionID, got.SessionAgent)
				}
			},
		},
		{
			name:  "mention posts comment.md as the reply",
			stage: types.StageMention,
			files: map[string]string{"comment.md": "it is the cache"},
			check: func(t *testing.T, store *Store, taskID, _ string) {
				comments, err := store.ListComments(t.Context(), taskID)
				if err != nil {
					t.Fatalf("ListComments: %v", err)
				}
				for _, c := range comments {
					if c.Source == SourceAgent && c.Body == "it is the cache" {
						return
					}
				}
				t.Fatalf("comments = %+v, want the mention's reply", comments)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, store, _ := newConvDispatcher(t)
			tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
			if err := store.Create(t.Context(), tk); err != nil {
				t.Fatalf("create: %v", err)
			}
			instanceID := "developer-task-" + tk.ID + "-" + string(tc.stage) + "-1727500000000"
			if tc.stage == types.StageMention {
				instanceID = "developer-mention-" + tk.ID + "-c0ffee-1727500000000"
			}
			if got, ok := InstanceTaskID(instanceID); !ok || got != tk.ID {
				t.Fatalf("InstanceTaskID(%q) = %q, %t; want %q", instanceID, got, ok, tk.ID)
			}
			outDir := filepath.Join(t.TempDir(), "data", "runs", instanceID)
			for name, body := range tc.files {
				if err := os.MkdirAll(outDir, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outDir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			ctx := context.Background()
			if tc.stage == types.StageMention {
				d.postMentionRun(ctx, tk.ID, "", "developer", "ctr-a", instanceID, outDir, 0)
			} else {
				d.postStageRun(ctx, tk.ID, tc.stage, "developer", "ctr-a", instanceID, outDir, 0,
					postRunOpts{NoAdvance: true})
			}
			tc.check(t, store, tk.ID, instanceID)
		})
	}
}

func TestInstanceTaskID(t *testing.T) {
	cases := []struct {
		instance string
		want     string
		ok       bool
	}{
		{"developer-task-abc123-in_progress-1727500000000", "abc123", true},
		{"code-reviewer-task-abc123-code_review-1727500000000", "abc123", true},
		{"qa-mention-abc123-c0ffee-1727500000000", "abc123", true},
		{"developer-chat-abc123", "", false},
		{"developer", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.instance, func(t *testing.T) {
			got, ok := InstanceTaskID(tc.instance)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("InstanceTaskID(%q) = %q, %t; want %q, %t", tc.instance, got, ok, tc.want, tc.ok)
			}
		})
	}
}
