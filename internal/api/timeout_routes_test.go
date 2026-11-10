package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsUnboundedRoute(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/events", true},
		{http.MethodGet, "/chats/events", true},
		{http.MethodGet, "/sandbox/sse", true},
		{http.MethodGet, "/audit/export", true},
		{http.MethodGet, "/git/task-abc/info/refs", true},
		{http.MethodPost, "/projects", true},
		{http.MethodPost, "/projects/upload", true},
		{http.MethodPost, "/projects/p-1/upload", true},
		{http.MethodGet, "/projects/p-1/archive", true},
		{http.MethodPost, "/git/task-abc/git-receive-pack", true},
		{http.MethodGet, "/task-attachments/att-1", true},
		{http.MethodPost, "/tasks/t-1/attachments", true},
		{http.MethodGet, "/agents/a-1/logs", true},
		{http.MethodGet, "/usage/sessions/s-1/logs", true},
		{http.MethodGet, "/usage/sessions/s-1/logs/stream", true},
		{http.MethodGet, "/mcp-proxy/thulu-github", true},
		{http.MethodPost, "/mcp-proxy/jira", true},

		{http.MethodGet, "/tasks", false},
		{http.MethodGet, "/tasks/t-1", false},
		{http.MethodGet, "/tasks/t-1/attachments", false},
		{http.MethodPost, "/chats/c-1/messages", false},
		{http.MethodPost, "/events/e-1/subscribe", false},
		{http.MethodGet, "/audit", false},
		{http.MethodGet, "/agents", false},
		{http.MethodGet, "/projects", false},
		{http.MethodPatch, "/projects/p-1", false},
		{http.MethodGet, "/projects/p-1/branches", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := isUnboundedRoute(r); got != c.want {
			t.Errorf("isUnboundedRoute(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
