package api

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/project"
)

func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

const (
	oidA    = "1111111111111111111111111111111111111111"
	oidB    = "2222222222222222222222222222222222222222"
	zeroOID = "0000000000000000000000000000000000000000"
)

func TestParseReceivePackCommands(t *testing.T) {
	body := pktLine("shallow "+oidA+"\n") +
		pktLine(oidA+" "+oidB+" refs/heads/task-1\x00report-status side-band-64k\n") +
		pktLine(zeroOID+" "+oidB+" refs/heads/other\n") +
		"0000" + "PACK...binary..."
	cmds, err := parseReceivePackCommands(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cmds) != 2 || cmds[0].Ref != "refs/heads/task-1" || cmds[1].Ref != "refs/heads/other" || cmds[0].New != oidB {
		t.Fatalf("commands = %+v", cmds)
	}

	for name, bad := range map[string]string{
		"truncated":   pktLine(oidA + " " + oidB + " refs/heads/x")[:20],
		"bad length":  "zzzz",
		"push-cert":   pktLine("push-cert\x00caps\n") + "0000",
		"no ref":      pktLine(oidA+" "+oidB+"\n") + "0000",
		"short frame": "0002",
	} {
		if _, err := parseReceivePackCommands(strings.NewReader(bad)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestRefusePushCommands(t *testing.T) {
	allowed := []string{"refs/heads/task-1"}
	cases := []struct {
		name string
		cmds []receivePackCommand
		ok   bool
	}{
		{"own branch", []receivePackCommand{{oidA, oidB, "refs/heads/task-1"}}, true},
		{"create own branch", []receivePackCommand{{zeroOID, oidB, "refs/heads/task-1"}}, true},
		{"default branch", []receivePackCommand{{oidA, oidB, "refs/heads/main"}}, false},
		{"another task", []receivePackCommand{{oidA, oidB, "refs/heads/task-2"}}, false},
		{"remote-tracking", []receivePackCommand{{oidA, oidB, "refs/remotes/origin/main"}}, false},
		{"delete own branch", []receivePackCommand{{oidA, zeroOID, "refs/heads/task-1"}}, false},
		{"own plus smuggled", []receivePackCommand{{oidA, oidB, "refs/heads/task-1"}, {oidA, oidB, "refs/heads/main"}}, false},
	}
	for _, c := range cases {
		if got := refusePushCommands(c.cmds, allowed) == nil; got != c.ok {
			t.Errorf("%s: allowed = %v, want %v", c.name, got, c.ok)
		}
	}
}

func TestReadReceivePackCommands_ReplaysTheBody(t *testing.T) {
	raw := pktLine(oidA+" "+oidB+" refs/heads/task-1\x00report-status\n") + "0000" + "PACKDATA"

	req := httptest.NewRequest(http.MethodPost, "/x/git-receive-pack", strings.NewReader(raw))
	body, cmds, err := readReceivePackCommands(req)
	if err != nil || len(cmds) != 1 {
		t.Fatalf("read: %v %+v", err, cmds)
	}
	if got, _ := io.ReadAll(body); string(got) != raw {
		t.Fatalf("replayed body = %q, want %q", got, raw)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(raw))
	_ = zw.Close()
	req = httptest.NewRequest(http.MethodPost, "/x/git-receive-pack", &gz)
	req.Header.Set("Content-Encoding", "gzip")
	body, cmds, err = readReceivePackCommands(req)
	if err != nil || len(cmds) != 1 {
		t.Fatalf("read gzip: %v %+v", err, cmds)
	}
	if got, _ := io.ReadAll(body); string(got) != raw {
		t.Fatalf("replayed gzip body = %q", got)
	}
	if req.Header.Get("Content-Encoding") != "" || req.ContentLength != -1 {
		t.Errorf("gzip body forwarded decoded but still labelled: enc=%q len=%d",
			req.Header.Get("Content-Encoding"), req.ContentLength)
	}
}

func TestGitTransport_PushConfinedToTheTasksBranch(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	srv, h := newIntegrationServer(t)
	if srv.authStore == nil {
		t.Skip("no auth store")
	}
	token, ws := mintWorkspaceUser(t, srv, "gitpush@test.com", "GP")
	projID := createProject(t, h, token, "pushguard")
	w := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "push", "project_ids": []string{projID}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", w.Code, w.Body.String())
	}
	taskID := decodeID(t, w.Body.Bytes())
	other := project.TaskBranchName("someone-else")

	proj, err := srv.projectStore.Get(t.Context(), projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	key, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: taskID, Agent: "developer"}, ws, time.Hour)
	if err != nil {
		t.Fatalf("mint run key: %v", err)
	}

	ts := httptest.NewServer(h)
	defer ts.Close()
	if resp, gerr := http.Get(ts.URL + "/git/"); gerr == nil {
		_ = resp.Body.Close()
	}
	cloneURL := strings.Replace(ts.URL, "http://", "http://octopus:"+key+"@", 1) +
		"/git/" + taskID + "/" + filepath.Base(proj.Path)

	work := filepath.Join(t.TempDir(), "clone")
	if out, cerr := runGitCmd("", "clone", cloneURL, work); cerr != nil {
		if strings.Contains(out, "404") || strings.Contains(out, "not found") {
			t.Skipf("git transport unavailable: %s", out)
		}
		t.Fatalf("clone: %v: %s", cerr, out)
	}
	mustGit := func(args ...string) {
		t.Helper()
		if out, gerr := runGitCmd(work, args...); gerr != nil {
			t.Fatalf("git %v: %v: %s", args, gerr, out)
		}
	}
	mustGit("config", "user.email", "agent@test")
	mustGit("config", "user.name", "agent")
	if err := os.WriteFile(filepath.Join(work, "out.txt"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", ".")
	mustGit("commit", "-m", "agent result")

	own := project.TaskBranchName(taskID)
	if out, perr := runGitCmd(work, "push", "origin", "HEAD:refs/heads/"+own); perr != nil {
		t.Fatalf("pushing the task's own branch failed: %v: %s", perr, out)
	}
	if out, perr := runGitCmd(work, "push", "origin", "HEAD:"+project.ResultBranchName(taskID)); perr != nil {
		t.Fatalf("pushing the task's result branch failed: %v: %s", perr, out)
	}
	for _, ref := range []string{
		"refs/heads/main",
		"refs/heads/" + other,
		"refs/heads/" + project.ResultBranchName("someone-else"),
		"refs/remotes/origin/main",
	} {
		if out, perr := runGitCmd(work, "push", "--force", "origin", "HEAD:"+ref); perr == nil {
			t.Errorf("push to %s was accepted: %s", ref, out)
		}
	}
	if out, perr := runGitCmd(work, "push", "origin", ":refs/heads/"+own); perr == nil {
		t.Errorf("deleting the task's branch was accepted: %s", out)
	}

	if out, _ := runGitCmd(proj.Path, "for-each-ref", "--format=%(refname)"); strings.Contains(out, other) ||
		strings.Contains(out, "refs/remotes/origin/main\n") && strings.Contains(out, "agent result") {
		t.Errorf("a refused push still landed: %s", out)
	}
	if out, _ := runGitCmd(proj.Path, "log", "-1", "--format=%s", "main"); strings.TrimSpace(out) == "agent result" {
		t.Errorf("main was overwritten by a refused push")
	}
}

func runGitCmd(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func decodeID(t *testing.T, b []byte) string {
	t.Helper()
	s := string(b)
	i := strings.Index(s, `"id":"`)
	if i < 0 {
		t.Fatalf("no id in %s", s)
	}
	s = s[i+6:]
	return s[:strings.Index(s, `"`)]
}
