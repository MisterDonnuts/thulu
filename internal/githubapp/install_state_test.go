package githubapp

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func stateService(t *testing.T) (*Service, *Store) {
	t.Helper()
	st := newTestStore(t)
	svc := NewService(st)
	return svc, st
}

func TestInstallStateIsSingleUseAndBound(t *testing.T) {
	svc, st := stateService(t)
	ctx := t.Context()

	state, binder, err := svc.IssueState(ctx, "ws-a", "user-1")
	if err != nil {
		t.Fatalf("IssueState: %v", err)
	}
	if state == "" || binder == "" || state == binder {
		t.Fatalf("state %q / binder %q", state, binder)
	}
	var n int
	if err := st.db.QueryRowContext(ctx,
		`SELECT count(*) FROM github_install_states WHERE state_hash = $1 OR binder_hash = $2`,
		state, binder).Scan(&n); err != nil || n != 0 {
		t.Fatalf("raw state or binder stored (n=%d err=%v)", n, err)
	}

	for _, b := range []string{"", "someone-elses-binder", state} {
		if _, err := svc.ConsumeState(ctx, state, b); !errors.Is(err, ErrStateInvalid) {
			t.Errorf("binder %q: err = %v, want ErrStateInvalid", b, err)
		}
	}

	flow, err := svc.ConsumeState(ctx, state, binder)
	if err != nil {
		t.Fatalf("ConsumeState: %v", err)
	}
	if flow.WorkspaceID != "ws-a" || flow.UserID != "user-1" {
		t.Errorf("flow = %+v", flow)
	}

	if _, err := svc.ConsumeState(ctx, state, binder); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("replayed state: err = %v, want ErrStateInvalid", err)
	}
	if _, err := svc.ConsumeState(ctx, "", binder); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("empty state: err = %v", err)
	}
	if _, _, err := svc.IssueState(ctx, "", "user-1"); err == nil {
		t.Error("a state for no workspace was issued")
	}
}

func TestInstallStateExpires(t *testing.T) {
	svc, st := stateService(t)
	ctx := t.Context()
	state, binder, err := svc.IssueState(ctx, "ws-a", "")
	if err != nil {
		t.Fatalf("IssueState: %v", err)
	}
	svc.now = func() time.Time { return time.Now().Add(InstallStateTTL + time.Minute) }
	if _, err := svc.ConsumeState(ctx, state, binder); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("expired state: err = %v, want ErrStateInvalid", err)
	}
	if _, _, err := svc.IssueState(ctx, "ws-b", ""); err != nil {
		t.Fatalf("IssueState: %v", err)
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM github_install_states WHERE workspace_id = 'ws-a'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("expired state still stored (n=%d err=%v)", n, err)
	}
}

func TestInstallStateRaceHasOneWinner(t *testing.T) {
	svc, _ := stateService(t)
	state, binder, err := svc.IssueState(t.Context(), "ws-a", "")
	if err != nil {
		t.Fatalf("IssueState: %v", err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.ConsumeState(t.Context(), state, binder); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d callbacks consumed the state, want 1", wins)
	}
}

func TestStateWithoutAStore(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, "", keyPEM)
	if _, _, err := svc.IssueState(t.Context(), "ws-a", ""); err == nil {
		t.Error("IssueState with no store succeeded")
	}
	if _, err := svc.ConsumeState(t.Context(), "s", "b"); err == nil {
		t.Error("ConsumeState with no store succeeded")
	}
}

func fakeGitHubUser(t *testing.T, login string, ids []int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login/oauth/access_token":
			if err := r.ParseForm(); err != nil || r.PostForm.Get("code") != "good-code" {
				_, _ = fmt.Fprint(w, `{"error":"bad_verification_code"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"access_token":"ghu_user"}`)
		case r.Header.Get("Authorization") != "token ghu_user":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/user":
			_, _ = fmt.Fprintf(w, `{"login":%q}`, login)
		case r.URL.Path == "/user/installations":
			page := 1
			_, _ = fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
			start, end := (page-1)*100, page*100
			if start > len(ids) {
				start = len(ids)
			}
			if end > len(ids) {
				end = len(ids)
			}
			parts := make([]string, 0, end-start)
			for _, id := range ids[start:end] {
				parts = append(parts, fmt.Sprintf(`{"id":%d}`, id))
			}
			_, _ = fmt.Fprintf(w, `{"total_count":%d,"installations":[%s]}`, len(ids), strings.Join(parts, ","))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVerifyInstaller(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	ids := make([]int64, 150)
	for i := range ids {
		ids[i] = int64(1000 + i)
	}
	gh := fakeGitHubUser(t, "octocat", ids)
	svc := envService(t, gh.URL, keyPEM)

	login, err := svc.VerifyInstaller(t.Context(), "good-code", 1120)
	if err != nil || login != "octocat" {
		t.Fatalf("VerifyInstaller = %q, %v", login, err)
	}
	if _, err := svc.VerifyInstaller(t.Context(), "good-code", 77); !errors.Is(err, ErrInstallerNotAuthorized) {
		t.Errorf("installation the user cannot see: err = %v, want ErrInstallerNotAuthorized", err)
	}
	if _, err := svc.VerifyInstaller(t.Context(), "stolen-code", 1120); err == nil || errors.Is(err, ErrInstallerNotAuthorized) {
		t.Errorf("bad code: err = %v, want an exchange failure", err)
	}
}

func TestClaimInstallationNeverTakesAnotherWorkspacesRow(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	if err := st.ClaimInstallation(ctx, Installation{InstallationID: 9, WorkspaceID: "ws-b", AccountLogin: "org-b"}); err != nil {
		t.Fatalf("claim by B: %v", err)
	}
	if err := st.ClaimInstallation(ctx, Installation{InstallationID: 9, WorkspaceID: "ws-a", AccountLogin: "org-b"}); !errors.Is(err, ErrInstallationClaimed) {
		t.Errorf("claim by A of B's row: err = %v, want ErrInstallationClaimed", err)
	}
	if err := st.ClaimInstallation(ctx, Installation{InstallationID: 9, WorkspaceID: "ws-b", RepoSelection: "all"}); err != nil {
		t.Errorf("refresh by B: %v", err)
	}
	if err := st.UpsertInstallation(ctx, Installation{InstallationID: 9, WorkspaceID: "ws-a"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := st.GetInstallation(ctx, 9)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.WorkspaceID != "ws-b" || got.RepoSelection != "all" || got.AccountLogin != "org-b" {
		t.Errorf("row = %+v, want B's, refreshed", got)
	}
	if err := st.ClaimInstallation(ctx, Installation{InstallationID: 10}); err == nil {
		t.Error("a claim for no workspace succeeded")
	}

	prev, err := st.ReassignInstallation(ctx, 9, "ws-a")
	if err != nil || prev != "ws-b" {
		t.Fatalf("reassign = %q, %v", prev, err)
	}
	if got, _ := st.GetInstallation(ctx, 9); got.WorkspaceID != "ws-a" {
		t.Errorf("after reassign wid = %q", got.WorkspaceID)
	}
	if _, err := st.ReassignInstallation(ctx, 404, "ws-a"); !errors.Is(err, ErrInstallationNotFound) {
		t.Errorf("reassign unknown: err = %v", err)
	}
}

func TestClaimInstallationRaceHasOneWinner(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertInstallation(t.Context(), Installation{InstallationID: 11, AccountLogin: "org"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins []string
	)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wid := fmt.Sprintf("ws-%d", i)
			if err := st.ClaimInstallation(t.Context(), Installation{InstallationID: 11, WorkspaceID: wid}); err == nil {
				mu.Lock()
				wins = append(wins, wid)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(wins) != 1 {
		t.Fatalf("winners = %v, want exactly one", wins)
	}
	if got, _ := st.GetInstallation(t.Context(), 11); got.WorkspaceID != wins[0] {
		t.Errorf("row wid = %q, winner %q", got.WorkspaceID, wins[0])
	}
}
