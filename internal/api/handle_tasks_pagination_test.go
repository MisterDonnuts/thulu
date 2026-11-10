package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

type pagedTask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

func listTasks(t *testing.T, h http.Handler, token, query string) ([]pagedTask, string) {
	t.Helper()
	w := getJSON(t, h, "/tasks"+query, token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks%s = %d: %s", query, w.Code, w.Body.String())
	}
	var out []pagedTask
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode /tasks%s: %v", query, err)
	}
	return out, w.Header().Get(nextCursorHeader)
}

func seedTasks(t *testing.T, h http.Handler, token string, n int) {
	t.Helper()
	for i := range n {
		w := postJSONAuth(t, h, "/tasks", token, map[string]any{
			"title":       "paged task",
			"body":        "a markdown description long enough to notice",
			"project_ids": []string{},
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("create task %d = %d: %s", i, w.Code, w.Body.String())
		}
	}
}

func TestIntegration_ListTasksOmitsBody(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "listbody@test.com", "L", "SecurePassword1")
	seedTasks(t, h, token, 2)

	tasks, cursor := listTasks(t, h, token, "")
	if len(tasks) != 2 {
		t.Fatalf("listed %d tasks, want 2", len(tasks))
	}
	if cursor != "" {
		t.Errorf("unpaginated request advertised a next page: %q", cursor)
	}
	for _, got := range tasks {
		if got.Body != "" {
			t.Errorf("list carried body %q for %s", got.Body, got.ID)
		}
	}

	w := getJSON(t, h, "/tasks/"+tasks[0].ID, token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks/{id} = %d", w.Code)
	}
	var detail pagedTask
	json.NewDecoder(w.Body).Decode(&detail)
	if detail.Body == "" {
		t.Error("GET /tasks/{id} returned no body; the drawer would render nothing")
	}
}

func TestIntegration_ListTasksPagesMatchWholeList(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "listpage@test.com", "P", "SecurePassword1")
	seedTasks(t, h, token, 5)

	whole, _ := listTasks(t, h, token, "")
	if len(whole) != 5 {
		t.Fatalf("unpaginated list = %d tasks, want 5", len(whole))
	}

	var walked []string
	query := "?limit=2"
	for range 10 {
		page, next := listTasks(t, h, token, query)
		if len(page) > 2 {
			t.Fatalf("page of %d rows exceeded ?limit=2", len(page))
		}
		for _, got := range page {
			walked = append(walked, got.ID)
		}
		if next == "" {
			break
		}
		query = "?limit=2&cursor=" + next
	}
	if len(walked) != len(whole) {
		t.Fatalf("walked %d tasks, unpaginated list has %d", len(walked), len(whole))
	}
	for i := range walked {
		if walked[i] != whole[i].ID {
			t.Errorf("page walk differs at %d: got %s want %s", i, walked[i], whole[i].ID)
		}
	}
}

func TestIntegration_ListTasksClampsLimit(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "listclamp@test.com", "C", "SecurePassword1")
	seedTasks(t, h, token, 3)

	tasks, cursor := listTasks(t, h, token, "?limit=100000")
	if len(tasks) != 3 {
		t.Fatalf("clamped request listed %d tasks, want 3", len(tasks))
	}
	if cursor != "" {
		t.Errorf("clamped request advertised a next page: %q", cursor)
	}

	if got, _ := listTasks(t, h, token, "?limit=0"); len(got) != 3 {
		t.Errorf("?limit=0 listed %d tasks, want the unpaginated 3", len(got))
	}
}

// Regression: the page window was cut across all workspaces and filtered afterwards, so a tenant
// got empty pages with cursors into other tenants' rows and could count them.
func TestIntegration_ListTasksCursorStaysInTheWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	mine, _ := mintWorkspaceUser(t, srv, "pageown@test.com", "Own")
	other, _ := mintWorkspaceUser(t, srv, "pageother@test.com", "Other")
	seedTasks(t, h, mine, 2)
	seedTasks(t, h, other, 3)

	pages, seen, cursor := 0, 0, ""
	for {
		q := "?limit=1"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		got, next := listTasks(t, h, mine, q)
		pages++
		if len(got) != 1 {
			t.Fatalf("page %d listed %d tasks, want exactly 1 of mine", pages, len(got))
		}
		seen += len(got)
		if next == "" {
			break
		}
		if pages > 5 {
			t.Fatalf("still paging after %d pages: the cursor walks foreign rows", pages)
		}
		cursor = next
	}
	if pages != 2 || seen != 2 {
		t.Errorf("paged %d pages / %d tasks, want 2 / 2", pages, seen)
	}
}

func TestIntegration_ListTasksRejectsBadCursor(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "listbadcur@test.com", "B", "SecurePassword1")
	seedTasks(t, h, token, 1)

	w := getJSON(t, h, "/tasks?limit=1&cursor=not-a-real-cursor!", token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor = %d, want 400: %s", w.Code, w.Body.String())
	}
}
