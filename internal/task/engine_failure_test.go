package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/engine"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	initLine       = `{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s-401","tools":[],"mcp_servers":[],"model":"claude-opus-4-7"}`
	revokedLine    = `{"type":"result","subtype":"success","is_error":true,"duration_ms":1890,"num_turns":1,"terminal_reason":"api_error","api_error_status":401,"result":"Failed to authenticate. API Error: 401 OAuth access token has been revoked.","session_id":"s-401","total_cost_usd":0}`
	successLine    = `{"type":"result","subtype":"success","is_error":false,"duration_ms":42000,"num_turns":7,"result":"done","session_id":"s-ok","total_cost_usd":0.4}`
	revokedMessage = "Failed to authenticate. OAuth access token has been revoked"
)

func resultOf(t *testing.T, lines ...string) engine.Event {
	t.Helper()
	ev, ok := engine.LastResult([]byte(strings.Join(lines, "\n")+"\n"), "")
	if !ok {
		t.Fatalf("no result in %q", lines)
	}
	return ev
}

func TestEngineFailureText(t *testing.T) {
	authNotice := i18n.Text("task.comments.run_engine_auth_failed", "status", 401, "message", revokedMessage)
	cases := []struct {
		name string
		line string
		want string
	}{
		{"revoked credential", revokedLine, authNotice},
		{
			"forbidden",
			`{"type":"result","is_error":true,"terminal_reason":"api_error","api_error_status":403,"result":"API Error: 403 {\"type\":\"error\",\"error\":{\"type\":\"permission_error\",\"message\":\"Your key cannot use this model\"}}"}`,
			i18n.Text("task.comments.run_engine_auth_failed", "status", 403, "message", "Your key cannot use this model"),
		},
		{
			"overloaded, older cli",
			`{"type":"result","subtype":"success","is_error":true,"result":"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"}`,
			i18n.Text("task.comments.run_engine_api_error", "status", 529, "message", "Overloaded"),
		},
		{
			"rate limited",
			`{"type":"result","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 Rate limit reached."}`,
			i18n.Text("task.comments.run_engine_api_error", "status", 429, "message", "Rate limit reached"),
		},
		{
			"no status",
			`{"type":"result","is_error":true,"terminal_reason":"api_error","result":"Connection error."}`,
			i18n.Text("task.comments.run_engine_api_failed", "message", "Connection error"),
		},
		{"plain failure", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`, ""},
		{"success", successLine, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := engineFailureText(resultOf(t, initLine, tc.line)).Text(); got != tc.want {
				t.Errorf("notice = %q\nwant     %q", got, tc.want)
			}
		})
	}
}

func TestCleanEngineMessageRedactsSecrets(t *testing.T) {
	in := "API Error: 401 invalid x-api-key sk-ant-api03-AbCdEf123456 (Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload) " +
		"token 0123456789abcdef0123456789abcdef0123."
	got := cleanEngineMessage(in)
	for _, leak := range []string{"sk-ant", "AbCdEf123456", "eyJhbGci", "0123456789abcdef0123456789abcdef"} {
		if strings.Contains(got, leak) {
			t.Errorf("message %q leaks %q", got, leak)
		}
	}
	if strings.Contains(got, "API Error") || strings.HasSuffix(got, ".") {
		t.Errorf("message %q keeps the status prefix or the trailing period", got)
	}
	long := cleanEngineMessage(strings.Repeat("word ", 200))
	if n := len([]rune(long)); n > engineMessageMax+1 {
		t.Errorf("message has %d runes, want at most %d", n, engineMessageMax+1)
	}
}

func writeRunFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func systemComments(t *testing.T, store *Store, taskID string) []string {
	t.Helper()
	comments, err := store.ListComments(t.Context(), taskID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	var out []string
	for _, c := range comments {
		if c.Source == SourceSystem {
			out = append(out, c.Body)
		}
	}
	return out
}

func TestAuditorSkipsARunTheProviderRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		log     string
		exit    int
		audited bool
	}{
		{"the provider refused", initLine + "\n" + revokedLine + "\n", 1, false},
		{"it never started", "", 90, false},
		{"it failed some other way, with work to judge", initLine + "\n" + `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":9}` + "\n", 1, true},
		{"it finished", initLine + "\n" + successLine + "\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, l, tk := auditedDispatcher(t)
			instance := "developer-task-" + tk.ID + "-" + string(types.StageInProgress) + "-1700000000000"
			outDir := filepath.Join(t.TempDir(), "runs", instance)
			if tc.log != "" {
				writeRunFile(t, outDir, "session.log", tc.log)
			}
			d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer", "ctr-a", instance, outDir, tc.exit, postRunOpts{})
			if got := len(l.launches()) == 1; got != tc.audited {
				t.Fatalf("auditor launched = %v, want %v", got, tc.audited)
			}
		})
	}
}

func TestAuditorWithoutVerdictSaysWhy(t *testing.T) {
	d, store, _, tk := auditedDispatcher(t)
	instance := "developer-audit-" + tk.ID + "-" + string(types.StageInProgress) + "-1700000000000"
	outDir := filepath.Join(t.TempDir(), "runs", instance)
	writeRunFile(t, outDir, "session.log", initLine+"\n"+revokedLine+"\n")

	d.finishAudit(context.Background(), tk.ID, types.StageInProgress,
		auditedRun{Agent: "developer", Instance: "x"}, "ctr-a", instance, outDir, 1)

	want := i18n.Text("task.comments.run_engine_auth_failed", "status", 401, "message", revokedMessage)
	if got := strings.Join(systemComments(t, store, tk.ID), "\n"); !strings.Contains(got, want) {
		t.Fatalf("comments = %q, want the reason %q", got, want)
	}
}

func TestCleanEngineMessageDropsRequestDetails(t *testing.T) {
	got := cleanEngineMessage("The model `x` does not exist., url: http://octopus:8585/llm/v1/responses, cf-ray: a5, request id: req_1")
	if got != "The model `x` does not exist" {
		t.Errorf("message = %q", got)
	}
}

func TestCleanEngineMessageKeepsAPlainLink(t *testing.T) {
	const link = "https://platform.openai.com/settings/organization/billing/"
	got := cleanEngineMessage("You have no credits remaining. Add credits at " + link + ".")
	if !strings.Contains(got, link) {
		t.Errorf("message %q lost the link", got)
	}
	secret := strings.Repeat("0123456789abcdef", 3)
	for _, leak := range []string{
		"https://example.com/hook/" + secret,
		"https://example.com/settings/organization/billing?token=" + secret,
	} {
		if got := cleanEngineMessage("see " + leak); strings.Contains(got, secret) {
			t.Errorf("message %q leaks %q", got, leak)
		}
	}
}

func TestPostMentionRunSeesAReplyPostedThroughTheAPI(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	instanceID := fmt.Sprintf("developer-mention-%s-c0ffee-%d", tk.ID, time.Now().Add(-time.Minute).UnixMilli())
	if _, err := store.AddComment(t.Context(), tk.ID, SourceAgent, "developer", "the answer"); err != nil {
		t.Fatalf("comment: %v", err)
	}

	d.postMentionRun(context.Background(), tk.ID, "", "developer", "ctr-a", instanceID, t.TempDir(), 0)

	noReply := i18n.Text("task.comments.mention_no_reply", "agent", "developer", "code", 0)
	if got := strings.Join(systemComments(t, store, tk.ID), "\n"); strings.Contains(got, noReply) {
		t.Fatalf("said no reply although the agent answered: %q", got)
	}

	later := fmt.Sprintf("developer-mention-%s-c0ffee-%d", tk.ID, time.Now().Add(time.Hour).UnixMilli())
	d.postMentionRun(context.Background(), tk.ID, "", "developer", "ctr-a", later, t.TempDir(), 0)
	if got := strings.Join(systemComments(t, store, tk.ID), "\n"); !strings.Contains(got, noReply) {
		t.Fatalf("an older comment passed for this run's reply: %q", got)
	}
}

func TestPlainLogTail(t *testing.T) {
	logs := "installing\n" + initLine + "\nline one\n\n2026-10-07T12:00:00Z " + initLine + "\nline two\nBun has crashed\nqemu: uncaught target signal 6\n"
	if got, want := plainLogTail(logs), "line two | Bun has crashed | qemu: uncaught target signal 6"; got != want {
		t.Errorf("tail = %q, want %q", got, want)
	}
	if got := plainLogTail(initLine + "\n"); got != "" {
		t.Errorf("a log of events only gave %q", got)
	}
}

func TestPostStageRunQuotesTheLogOfACrashedEngine(t *testing.T) {
	const secret = "sk-ant-abcdefghijklmnop"
	cases := []struct {
		name   string
		runLog string
		want   string
	}{
		{"crash", initLine + "\nBun has crashed\nkey " + secret + "\n", i18n.Text("task.comments.run_engine_crashed", "message", "Bun has crashed | key [redacted]")},
		{"nothing printed", "", i18n.Text("task.comments.run_engine_crashed_silent")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, store, l := newConvDispatcher(t)
			d.launcher = &logLauncher{convLauncher: l, logs: map[string]string{"ctr-a": tc.runLog}}
			tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
			if err := store.Create(t.Context(), tk); err != nil {
				t.Fatalf("create: %v", err)
			}
			instanceID := "developer-task-" + tk.ID + "-in_progress-1727500000000"
			outDir := filepath.Join(t.TempDir(), "runs", instanceID)

			d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer", "ctr-a", instanceID, outDir, 130,
				postRunOpts{NoAdvance: true})

			all := strings.Join(systemComments(t, store, tk.ID), "\n")
			if !strings.Contains(all, tc.want) {
				t.Errorf("comments %q do not say %q", all, tc.want)
			}
			if strings.Contains(all, secret) {
				t.Errorf("comments leak the key: %q", all)
			}
		})
	}
}

func TestPostStageRunSaysTheEngineRejectedTheCredential(t *testing.T) {
	authNotice := i18n.Text("task.comments.run_engine_auth_failed", "status", 401, "message", revokedMessage)
	cases := []struct {
		name       string
		sessionLog string
		runLog     string
		exit       int
		want       bool
	}{
		{"docker: session.log", initLine + "\n" + revokedLine + "\n", "", 1, true},
		{"kubernetes: run log", "", "2026-10-07T12:00:00Z " + initLine + "\n2026-10-07T12:00:02Z " + revokedLine + "\n", 1, true},
		{"successful run", initLine + "\n" + successLine + "\n", "", 0, false},
		{"failed without an api error", initLine + "\n" + `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}` + "\n", "", 1, false},
		{"no output at all", "", "", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, store, l := newConvDispatcher(t)
			d.launcher = &logLauncher{convLauncher: l, logs: map[string]string{"ctr-a": tc.runLog}}
			tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
			if err := store.Create(t.Context(), tk); err != nil {
				t.Fatalf("create: %v", err)
			}
			instanceID := "developer-task-" + tk.ID + "-in_progress-1727500000000"
			outDir := filepath.Join(t.TempDir(), "runs", instanceID)
			if tc.sessionLog != "" {
				writeRunFile(t, outDir, "session.log", tc.sessionLog)
			}

			d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer", "ctr-a", instanceID, outDir, tc.exit,
				postRunOpts{NoAdvance: true})

			finished := i18n.Text("task.comments.run_finished", "stage", d.stageName(tk, types.StageInProgress), "code", tc.exit, "count", 0)
			var closing string
			for _, body := range systemComments(t, store, tk.ID) {
				if strings.HasPrefix(body, finished) {
					closing = body
				}
			}
			if closing == "" {
				t.Fatalf("no closing comment starting with %q in %q", finished, systemComments(t, store, tk.ID))
			}
			if got := strings.Contains(closing, authNotice); got != tc.want {
				t.Fatalf("closing comment = %q; notice present = %v, want %v", closing, got, tc.want)
			}
			if n := strings.Count(strings.Join(systemComments(t, store, tk.ID), "\n"), authNotice); tc.want && n != 1 {
				t.Fatalf("the notice appears %d times, want once", n)
			}
		})
	}
}

func TestPostMentionRunSaysTheEngineRejectedTheCredential(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	instanceID := "developer-mention-" + tk.ID + "-c0ffee-1727500000000"
	outDir := filepath.Join(t.TempDir(), "runs", instanceID)
	writeRunFile(t, outDir, "session.log", initLine+"\n"+revokedLine+"\n")

	d.postMentionRun(context.Background(), tk.ID, "", "developer", "ctr-a", instanceID, outDir, 1)

	want := i18n.Text("task.comments.mention_no_reply", "agent", "developer", "code", 1) + " " +
		i18n.Text("task.comments.run_engine_auth_failed", "status", 401, "message", revokedMessage)
	for _, body := range systemComments(t, store, tk.ID) {
		if body == want {
			return
		}
	}
	t.Fatalf("system comments = %q, want %q", systemComments(t, store, tk.ID), want)
}

func TestPostStageRunReportsTheProfileOutcome(t *testing.T) {
	cases := []struct {
		name       string
		sessionLog string
		exit       int
		profile    string
		want       []int
	}{
		{"refused key", initLine + "\n" + revokedLine + "\n", 1, "lp_1", []int{401}},
		{"clean run", initLine + "\n" + successLine + "\n", 0, "lp_1", []int{0}},
		{"failed without an api error", initLine + "\n" + `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}` + "\n", 1, "lp_1", nil},
		{"rate limited", initLine + "\n" + `{"type":"result","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 Rate limit reached."}` + "\n", 1, "lp_1", nil},
		{"no profile", initLine + "\n" + revokedLine + "\n", 1, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, store, _ := newConvDispatcher(t)
			var got []int
			d.WithLLMRefusals(func(_ context.Context, ws, profileID string, status int) {
				if ws != testWS || profileID != tc.profile {
					t.Errorf("reported ws %q profile %q", ws, profileID)
				}
				got = append(got, status)
			})
			if tc.profile != "" {
				d.runProfiles.Store("ctr-a", tc.profile)
			}
			tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
			if err := store.Create(t.Context(), tk); err != nil {
				t.Fatalf("create: %v", err)
			}
			instanceID := "developer-task-" + tk.ID + "-in_progress-1727500000000"
			outDir := filepath.Join(t.TempDir(), "runs", instanceID)
			writeRunFile(t, outDir, "session.log", tc.sessionLog)

			d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer", "ctr-a", instanceID, outDir, tc.exit,
				postRunOpts{NoAdvance: true})

			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("reported %v, want %v", got, tc.want)
			}
		})
	}
}
