package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenPR_ValidationErrors(t *testing.T) {
	ctx := context.Background()

	if _, err := OpenPR(ctx, "", OpenPRReq{Owner: "o", Repo: "r", Head: "h", Base: "b", Title: "t"}); err == nil {
		t.Error("expected error for empty token")
	}
	if _, err := OpenPR(ctx, "tok", OpenPRReq{}); err == nil {
		t.Error("expected error for missing owner/repo/head/base/title")
	}
}

func openPRWithServer(t *testing.T, ts *httptest.Server) (*OpenPRResp, error) {
	t.Helper()
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = rewriteTransport{base: ts.Client().Transport, target: ts.URL}
	return OpenPR(context.Background(), "fake-token", OpenPRReq{
		Owner: "owner", Repo: "repo", Head: "feature", Base: "main", Title: "My PR",
	})
}

type rewriteTransport struct {
	base   http.RoundTripper
	target string
}

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme = "http"
	r2.URL.Host = rt.target[len("http://"):]
	return rt.base.RoundTrip(r2)
}

func TestOpenPR_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "https://github.com/owner/repo/pull/42",
		})
	}))
	defer ts.Close()

	resp, err := openPRWithServer(t, ts)
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if resp.Number != 42 {
		t.Errorf("Number = %d, want 42", resp.Number)
	}
}

func TestOpenPR_AlreadyExists(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Validation Failed",
			"errors": []map[string]string{
				{"message": "A pull request already exists for owner:feature."},
			},
		})
	}))
	defer ts.Close()

	_, err := openPRWithServer(t, ts)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestOpenPR_OtherError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible"})
	}))
	defer ts.Close()

	_, err := openPRWithServer(t, ts)
	if err == nil {
		t.Error("expected error for 403")
	}
}

func TestOpenPR_EmptyBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	_, err := openPRWithServer(t, ts)
	if err == nil {
		t.Error("expected error for 500 empty body")
	}
}

func TestOpenPR_NoCommitsBetween(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Validation Failed",
			"errors": []map[string]string{
				{"message": "No commits between main and feature."},
			},
		})
	}))
	defer ts.Close()

	_, err := openPRWithServer(t, ts)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists for 'No commits between', got %v", err)
	}
}

func TestOpenPR_BadJSONOnCreated(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("not valid json"))
	}))
	defer ts.Close()

	_, err := openPRWithServer(t, ts)
	if err == nil {
		t.Error("expected error when 201 response body is invalid JSON")
	}
}

func TestParsePRError(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		body          string
		wantSentinel  bool
		wantSubstring string
	}{
		{
			name:         "422 already exists → ErrAlreadyExists",
			status:       http.StatusUnprocessableEntity,
			body:         `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for owner:feature."}]}`,
			wantSentinel: true,
		},
		{
			name:         "422 no commits between → ErrAlreadyExists",
			status:       http.StatusUnprocessableEntity,
			body:         `{"message":"Validation Failed","errors":[{"message":"No commits between main and feature."}]}`,
			wantSentinel: true,
		},
		{
			name:          "422 other validation error stays an error",
			status:        http.StatusUnprocessableEntity,
			body:          `{"message":"Validation Failed","errors":[{"message":"base invalid"}]}`,
			wantSubstring: "base invalid",
		},
		{
			name:          "403 with message",
			status:        http.StatusForbidden,
			body:          `{"message":"Resource not accessible"}`,
			wantSubstring: "Resource not accessible",
		},
		{
			name:          "unparseable body falls back to HTTP status",
			status:        http.StatusInternalServerError,
			body:          "",
			wantSubstring: "HTTP 500",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := parsePRError(c.status, []byte(c.body))
			if err == nil {
				t.Fatal("parsePRError must always return an error for non-201")
			}
			if got := errors.Is(err, ErrAlreadyExists); got != c.wantSentinel {
				t.Errorf("errors.Is(err, ErrAlreadyExists) = %v, want %v (err=%v)", got, c.wantSentinel, err)
			}
			if c.wantSubstring != "" && !strings.Contains(err.Error(), c.wantSubstring) {
				t.Errorf("error %q does not contain %q", err, c.wantSubstring)
			}
		})
	}
}

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in    string
		owner string
		repo  string
		ok    bool
	}{
		{"https://github.com/acme/web.git", "acme", "web", true},
		{"https://github.com/acme/web", "acme", "web", true},
		{"git@github.com:acme/web.git", "acme", "web", true},
		{"git@github-alias:acme/backoffice.git", "acme", "backoffice", true},
		{"git@github.com:foo/bar", "foo", "bar", true},
		{"", "", "", false},
		{"not a url", "", "", false},
		{"https://github.com/", "", "", false},
		{"git@github.com:onlyone", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			o, r, ok := ParseRemote(c.in)
			if ok != c.ok || o != c.owner || r != c.repo {
				t.Errorf("ParseRemote(%q) = (%q, %q, %v); want (%q, %q, %v)",
					c.in, o, r, ok, c.owner, c.repo, c.ok)
			}
		})
	}
}

func findPRWithServer(t *testing.T, ts *httptest.Server) (*OpenPRResp, error) {
	t.Helper()
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = rewriteTransport{base: ts.Client().Transport, target: ts.URL}
	return FindPRByHead(context.Background(), "fake-token", "owner", "repo", "feature")
}

func TestFindPRByHead_Found(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":53,"html_url":"https://github.com/owner/repo/pull/53"}]`))
	}))
	defer ts.Close()

	got, err := findPRWithServer(t, ts)
	if err != nil {
		t.Fatalf("FindPRByHead: %v", err)
	}
	if got == nil || got.Number != 53 || got.HTMLURL != "https://github.com/owner/repo/pull/53" {
		t.Fatalf("got %+v, want PR 53", got)
	}
	// Without the owner: prefix GitHub ignores head= and returns every open PR, attaching an
	// unrelated PR to the task.
	if !strings.Contains(gotQuery, "head=owner%3Afeature") {
		t.Errorf("query = %q, want head=owner:feature (url-encoded)", gotQuery)
	}
	if !strings.Contains(gotQuery, "state=open") {
		t.Errorf("query = %q, want state=open", gotQuery)
	}
}

func TestFindPRByHead_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	got, err := findPRWithServer(t, ts)
	if err != nil {
		t.Fatalf("FindPRByHead: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil for no match", got)
	}
}

func TestFindPRByHead_Errors(t *testing.T) {
	ctx := context.Background()
	if _, err := FindPRByHead(ctx, "", "o", "r", "b"); err == nil {
		t.Error("empty token: want error")
	}
	if _, err := FindPRByHead(ctx, "tok", "", "r", "b"); err == nil {
		t.Error("empty owner: want error")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer ts.Close()
	if _, err := findPRWithServer(t, ts); err == nil {
		t.Error("HTTP 404: want error")
	}
}

func TestPRCallsFollowConfiguredGitHub(t *testing.T) {
	t.Setenv("OCTOPUS_GITHUB_URL", "https://ghe.example.com")
	t.Setenv("OCTOPUS_GITHUB_API_URL", "")
	var gotPath, gotHost string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"number": 1})
	}))
	defer ts.Close()
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	base := rewriteTransport{base: ts.Client().Transport, target: ts.URL}
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotHost = r.URL.Host
		return base.RoundTrip(r)
	})

	_, err := OpenPR(context.Background(), "tok", OpenPRReq{
		Owner: "own", Repo: "re/../../admin?x=", Head: "h", Base: "main", Title: "t",
	})
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if gotHost != "ghe.example.com" {
		t.Errorf("host = %q, want ghe.example.com", gotHost)
	}
	if want := "/api/v3/repos/own/re%2F..%2F..%2Fadmin%3Fx=/pulls"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
