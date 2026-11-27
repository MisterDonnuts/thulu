package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMarkPRReady_RunsTheMutationOnADraft(t *testing.T) {
	var calls []string
	var mutation string
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/7":
			w.Write([]byte(`{"number":7,"node_id":"PR_kw7","state":"open","draft":true}`))
		case "/graphql":
			b, _ := io.ReadAll(r.Body)
			mutation = string(b)
			w.Write([]byte(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`))
		default:
			http.NotFound(w, r)
		}
	})
	changed, err := MarkPRReady(context.Background(), "tok", "acme", "widgets", 7)
	if err != nil || !changed {
		t.Fatalf("MarkPRReady = %v, %v; calls %v", changed, err, calls)
	}
	if !strings.Contains(mutation, "markPullRequestReadyForReview") || !strings.Contains(mutation, `"id":"PR_kw7"`) {
		t.Errorf("mutation body = %s", mutation)
	}
}

func TestMarkPRReady_LeavesANonDraftAlone(t *testing.T) {
	graphql := false
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			graphql = true
		}
		w.Write([]byte(`{"number":7,"node_id":"PR_kw7","state":"open","draft":false}`))
	})
	changed, err := MarkPRReady(context.Background(), "tok", "acme", "widgets", 7)
	if err != nil || changed || graphql {
		t.Errorf("MarkPRReady on a ready PR = %v, %v (graphql called: %v)", changed, err, graphql)
	}
}

func TestMarkPRReady_ReportsGraphQLErrors(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			w.Write([]byte(`{"data":{"markPullRequestReadyForReview":null},"errors":[{"message":"Resource not accessible by integration"}]}`))
			return
		}
		w.Write([]byte(`{"number":7,"node_id":"PR_kw7","state":"open","draft":true}`))
	})
	changed, err := MarkPRReady(context.Background(), "tok", "acme", "widgets", 7)
	if err == nil || changed || !strings.Contains(err.Error(), "Resource not accessible") {
		t.Errorf("MarkPRReady = %v, %v; want the GraphQL error", changed, err)
	}
}

func TestOpenPR_DraftUnsupported(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"Draft pull requests are not supported in this repository."}]}`))
	})
	_, err := OpenPR(context.Background(), "tok", OpenPRReq{
		Owner: "acme", Repo: "widgets", Head: "h", Base: "main", Title: "t", Draft: true,
	})
	if !errors.Is(err, ErrDraftUnsupported) {
		t.Errorf("err = %v, want ErrDraftUnsupported", err)
	}
}
