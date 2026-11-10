package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func fakeAPI(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", ts.URL)
}

func TestMergePR(t *testing.T) {
	var got string
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.Path + " " + string(b)
		w.Write([]byte(`{"merged":true,"sha":"abc","message":"ok"}`))
	})
	res, err := MergePR(context.Background(), "tok", &MergeReq{Owner: "acme", Repo: "widgets", Number: 7, Method: "rebase"})
	if err != nil || !res.Merged || res.SHA != "abc" {
		t.Fatalf("MergePR = %+v, %v", res, err)
	}
	if got != `PUT /repos/acme/widgets/pulls/7/merge {"merge_method":"rebase"}` {
		t.Errorf("request = %q", got)
	}

	if _, err := MergePR(context.Background(), "tok", &MergeReq{Owner: "acme", Repo: "widgets", Number: 7, Method: "octopus"}); err == nil {
		t.Error("an unknown merge method went through")
	}
}

func TestMergePR_NotMergeableIsAConflict(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte(`{"message":"Pull Request is not mergeable"}`))
	})
	_, err := MergePR(context.Background(), "tok", &MergeReq{Owner: "acme", Repo: "widgets", Number: 7})
	if apperr.KindOf(err) != apperr.KindConflict || !strings.Contains(err.Error(), "not mergeable") {
		t.Errorf("err = %v (kind %v), want a conflict carrying GitHub's message", err, apperr.KindOf(err))
	}
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Key != "github.errors.api" {
		t.Errorf("err = %#v, want github.errors.api", err)
	}
}

func TestReviewPendingDeployments(t *testing.T) {
	var body string
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`[{"environment":{"id":5,"name":"prod"},"current_user_can_approve":true}]`))
		case http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			body = string(b)
			w.Write([]byte(`[{"id":11},{"id":12}]`))
		}
	})
	pending, err := PendingDeployments(context.Background(), "tok", "acme", "widgets", 99)
	if err != nil || len(pending) != 1 || pending[0].Environment.ID != 5 || !pending[0].CurrentUserCanApprove {
		t.Fatalf("PendingDeployments = %+v, %v", pending, err)
	}
	ids, err := ReviewPendingDeployments(context.Background(), "tok", &DeploymentReview{
		Owner: "acme", Repo: "widgets", RunID: 99, EnvironmentIDs: []int64{5}, State: "rejected", Comment: "no",
	})
	if err != nil || len(ids) != 2 || ids[0] != 11 {
		t.Fatalf("ReviewPendingDeployments = %v, %v", ids, err)
	}
	if body != `{"comment":"no","environment_ids":[5],"state":"rejected"}` {
		t.Errorf("review body = %s", body)
	}

	if _, err := ReviewPendingDeployments(context.Background(), "tok", &DeploymentReview{
		Owner: "acme", Repo: "widgets", RunID: 99, State: "approved",
	}); err == nil {
		t.Error("a review naming no environment went through")
	}
}
