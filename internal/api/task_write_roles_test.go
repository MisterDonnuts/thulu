package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

// The task screens choose which buttons to show from these roles; a change here must be mirrored in
// the UI.
var taskWriteRoles = map[string]auth.Role{
	"POST /tasks":                                               auth.RoleDeveloper,
	"PATCH /tasks/{id}":                                         auth.RoleDeveloper,
	"DELETE /tasks/{id}":                                        auth.RoleAdmin,
	"DELETE /tasks/{id}/sandbox":                                auth.RoleAdmin,
	"DELETE /tasks/{id}/sandbox/queue":                          auth.RoleDeveloper,
	"DELETE /tasks/{id}/orphan-agents/{containerId}":            auth.RoleDeveloper,
	"POST /tasks/{id}/queue/cancel":                             auth.RoleDeveloper,
	"POST /tasks/{id}/subtasks":                                 auth.RoleDeveloper,
	"PUT /tasks/{id}/subtasks/order":                            auth.RoleDeveloper,
	"POST /tasks/{id}/clone":                                    auth.RoleDeveloper,
	"POST /tasks/{id}/fork":                                     auth.RoleDeveloper,
	"POST /tasks/{id}/relaunch":                                 auth.RoleDeveloper,
	"POST /tasks/{id}/ensure-pr":                                auth.RoleDeveloper,
	"POST /tasks/{id}/merge":                                    auth.RoleDeveloper,
	"POST /tasks/{id}/comments":                                 auth.RoleDeveloper,
	"POST /tasks/{id}/threads/{threadId}/apply":                 auth.RoleDeveloper,
	"POST /tasks/{id}/threads/{threadId}/resolve":               auth.RoleDeveloper,
	"POST /tasks/{id}/converse":                                 auth.RoleDeveloper,
	"POST /tasks/{id}/conversation/{msg}/to-comment":            auth.RoleDeveloper,
	"PUT /tasks/{id}/session-snapshot":                          auth.RoleDeveloper,
	"POST /tasks/{id}/attachments":                              auth.RoleDeveloper,
	"DELETE /task-attachments/{attachmentId}":                   auth.RoleDeveloper,
	"POST /tasks/{id}/reports":                                  auth.RoleDeveloper,
	"POST /tasks/{id}/flags":                                    auth.RoleDeveloper,
	"POST /tasks/{id}/flags/import":                             auth.RoleDeveloper,
	"POST /tasks/{id}/auditor-feedback":                         auth.RoleDeveloper,
	"PATCH /tasks/{id}/flags/{flagId}":                          auth.RoleDeveloper,
	"DELETE /tasks/{id}/flags/{flagId}":                         auth.RoleDeveloper,
	"PUT /tasks/{id}/sections/{slug}":                           auth.RoleDeveloper,
	"DELETE /tasks/{id}/sections/{slug}":                        auth.RoleDeveloper,
	"PATCH /tasks/{id}/sections/{slug}/items/{index}":           auth.RoleDeveloper,
	"POST /tasks/{id}/dependencies":                             auth.RoleDeveloper,
	"DELETE /tasks/{id}/dependencies/{depId}":                   auth.RoleDeveloper,
	"POST /tasks/{id}/questions":                                auth.RoleDeveloper,
	"POST /tasks/{id}/questions/{qid}/answer":                   auth.RoleDeveloper,
	"POST /tasks/{id}/questions/{qid}/cancel":                   auth.RoleDeveloper,
	"POST /tasks/{id}/approvals":                                auth.RoleDeveloper,
	"POST /tasks/{id}/approvals/{aid}/decide":                   auth.RoleDeveloper,
	"DELETE /tasks/{id}/approvals/{aid}":                        auth.RoleDeveloper,
	"POST /tasks/{id}/environments/{envId}/mapping-proposals":   auth.RoleDeveloper,
	"POST /tasks/{id}/environments/{envId}/dashboard-proposals": auth.RoleDeveloper,
	"POST /tasks/{id}/test-cases":                               auth.RoleDeveloper,
	"POST /tasks/{id}/test-cases/import-body":                   auth.RoleDeveloper,
	"PATCH /tasks/{id}/test-cases/{case}":                       auth.RoleDeveloper,
	"DELETE /tasks/{id}/test-cases/{case}":                      auth.RoleDeveloper,
	"POST /tasks/{id}/test-cases/{case}/runs":                   auth.RoleDeveloper,
}

var taskWriteRoute = regexp.MustCompile(`HandleFunc\("((?:POST|PATCH|PUT|DELETE) /task(?:s|-attachments)(?:[/?][^"]*)?)",\s*rr\(auth\.(Role\w+)`)

func TestTaskWriteRoutesMatchTheTable(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]auth.Role{"RoleViewer": auth.RoleViewer, "RoleDeveloper": auth.RoleDeveloper, "RoleAdmin": auth.RoleAdmin, "RoleOwner": auth.RoleOwner}
	found := map[string]auth.Role{}
	unguarded := regexp.MustCompile(`HandleFunc\("((?:POST|PATCH|PUT|DELETE) /task(?:s|-attachments)[^"]*)",\s*s\.`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range taskWriteRoute.FindAllSubmatch(src, -1) {
			found[string(m[1])] = roles[string(m[2])]
		}
		for _, m := range unguarded.FindAllSubmatch(src, -1) {
			t.Errorf("%s: %s is registered without a role", f, m[1])
		}
	}
	var missing, extra []string
	for route, want := range taskWriteRoles {
		got, ok := found[route]
		switch {
		case !ok:
			missing = append(missing, route)
		case got != want:
			t.Errorf("%s needs %s, the table says %s", route, got, want)
		}
	}
	for route := range found {
		if _, ok := taskWriteRoles[route]; !ok {
			extra = append(extra, route)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("in the table but not registered: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("registered but not in the table (add each with its role): %v", extra)
	}
}

func TestTaskWriteRolesRefuseTheRoleBelow(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokens := map[auth.Role]string{}
	for _, role := range []auth.Role{auth.RoleViewer, auth.RoleDeveloper, auth.RoleAdmin} {
		tokens[role], _ = mintWorkspaceUserAs(t, srv, "roles-"+string(role)+"@test.com", "Roles "+string(role), role)
	}
	below := map[auth.Role]auth.Role{auth.RoleDeveloper: auth.RoleViewer, auth.RoleAdmin: auth.RoleDeveloper}
	fill := strings.NewReplacer("{id}", "t-none", "{threadId}", "c-none", "{msg}", "m-none",
		"{attachmentId}", "a-none", "{flagId}", "f-none", "{slug}", "s-none", "{index}", "0",
		"{depId}", "t-other", "{qid}", "q-none", "{aid}", "ap-none", "{envId}", "e-none", "{case}", "k-none")

	roleRefusal := func(w *httptest.ResponseRecorder) bool {
		var b struct {
			Code string `json:"code"`
		}
		return w.Code == http.StatusForbidden && json.Unmarshal(w.Body.Bytes(), &b) == nil &&
			b.Code == "auth.errors.insufficient_permissions"
	}
	do := func(method, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader([]byte("{}")))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	for route, need := range taskWriteRoles {
		method, pattern, _ := strings.Cut(route, " ")
		path := fill.Replace(pattern)
		if w := do(method, path, tokens[below[need]]); !roleRefusal(w) {
			t.Errorf("%s as %s = %d %s, want the role refusal", route, below[need], w.Code, w.Body.String())
		}
		if w := do(method, path, tokens[need]); roleRefusal(w) {
			t.Errorf("%s as %s was refused for the role", route, need)
		}
	}
}
