package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/gitremote"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
)

// /git/ bypasses the env-API-key gate (see buildHandler) because git sends Basic auth; gitAuthorize
// enforces a task-scoped token on every request.
func (s *Server) registerGitRoutes(mux *http.ServeMux) {
	if s.authStore == nil || s.taskStore == nil || s.projectStore == nil {
		return
	}
	h, err := gitremote.New("", s.authorizeGitRequest)
	if err != nil {
		log.Printf("[git] %s", i18n.Log("api.git.log.transport_disabled", "err", err))
		return
	}
	h.WithResolver(s.resolveGitRepo)
	mux.Handle("/git/", http.StripPrefix("/git", s.guardGitPush(h)))
	log.Printf("[git] %s", i18n.Log("api.git.log.transport_mounted", "path", "/git/"))
}

// All tasks of a project share this repo with receive-pack on, and git-http-backend applies any ref
// update sent: refuse unless every command updates the task's own branch.
// Deletes refused; force-push allowed (rebases). Not a pre-receive hook: it would live in a repo
// the agent can push into.
func (s *Server) guardGitPush(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoPath, isPush := strings.CutSuffix(r.URL.Path, "/git-receive-pack")
		if !isPush || r.Method != http.MethodPost {
			if refsPath, ok := strings.CutSuffix(r.URL.Path, "/info/refs"); ok {
				if refsPath = strings.Trim(refsPath, "/"); s.authorizeGitRequest(r, refsPath) {
					s.refreshDefaultBeforeFetch(r, refsPath)
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		repoPath = strings.Trim(repoPath, "/")
		// Pass unauthorized requests through so the transport sends the Basic challenge git needs
		// to retry with credentials.
		if !s.authorizeGitRequest(r, repoPath) {
			next.ServeHTTP(w, r)
			return
		}
		taskID, _, _ := strings.Cut(repoPath, "/")
		allowed := s.gitPushableRefs(r.Context(), taskID)
		body, cmds, err := readReceivePackCommands(r)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.git.errors.malformed_push"))
			return
		}
		if err := refusePushCommands(cmds, allowed); err != nil {
			writeError(w, r, http.StatusForbidden, err)
			return
		}
		r.Body = body
		next.ServeHTTP(w, r)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentPublishTimeout)
		defer cancel()
		s.publishAfterAgentPush(ctx, repoPath, cmds)
	})
}

// Includes the result branch result-push.sh pushes to in Kubernetes mode, named after the sandbox
// root (a subtask pushes to its parent's).
func (s *Server) gitPushableRefs(ctx context.Context, taskID string) []string {
	refs := []string{
		"refs/heads/" + project.TaskBranchName(taskID),
		"refs/heads/" + project.ResultBranchName(taskID),
	}
	t, err := s.taskStore.GetRow(ctx, taskID)
	if err != nil || t == nil {
		return refs
	}
	if t.BranchName != "" {
		refs = append(refs, "refs/heads/"+t.BranchName)
	}
	if root := t.SandboxRootID(); root != taskID {
		refs = append(refs, "refs/heads/"+project.ResultBranchName(root))
	}
	return refs
}

type receivePackCommand struct {
	Old, New, Ref string
}

// A task pushes one branch; the bound only stops a hostile request from making the daemon buffer an
// unbounded command list.
const maxPushCommands = 64

func readReceivePackCommands(r *http.Request) (io.ReadCloser, []receivePackCommand, error) {
	src := io.Reader(r.Body)
	closer := r.Body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, nil, apperr.Wrap(err, "api.git.errors.gzip")
		}
		src = gz
		r.Header.Del("Content-Encoding")
		r.Header.Del("Content-Length")
		r.ContentLength = -1
	}
	var consumed bytes.Buffer
	cmds, err := parseReceivePackCommands(io.TeeReader(src, &consumed))
	if err != nil {
		return nil, nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(&consumed, src), closer}, cmds, nil
}

func parseReceivePackCommands(rd io.Reader) ([]receivePackCommand, error) {
	var cmds []receivePackCommand
	var head [4]byte
	for {
		if _, err := io.ReadFull(rd, head[:]); err != nil {
			return nil, apperr.Wrap(err, "api.git.errors.read_pkt_length")
		}
		n, err := strconv.ParseUint(string(head[:]), 16, 16)
		if err != nil {
			return nil, apperr.Invalid("api.git.errors.bad_pkt_length_text", "length", string(head[:]))
		}
		if n == 0 {
			return cmds, nil
		}
		if n < 4 {
			return nil, apperr.Invalid("api.git.errors.bad_pkt_length", "length", n)
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(rd, line); err != nil {
			return nil, apperr.Wrap(err, "api.git.errors.read_pkt_line")
		}
		text, _, _ := strings.Cut(string(line), "\x00")
		text = strings.TrimSuffix(text, "\n")
		if strings.HasPrefix(text, "shallow ") {
			continue
		}
		if strings.HasPrefix(text, "push-cert") {
			return nil, apperr.Invalid("api.git.errors.signed_push_unsupported")
		}
		parts := strings.SplitN(text, " ", 3)
		if len(parts) != 3 || parts[2] == "" {
			return nil, apperr.Invalid("api.git.errors.bad_command", "command", text)
		}
		cmds = append(cmds, receivePackCommand{Old: parts[0], New: parts[1], Ref: parts[2]})
		if len(cmds) > maxPushCommands {
			return nil, apperr.Invalid("api.git.errors.too_many_ref_updates", "max", maxPushCommands)
		}
	}
}

func refusePushCommands(cmds []receivePackCommand, allowed []string) error {
	for _, c := range cmds {
		if !slices.Contains(allowed, c.Ref) {
			return apperr.Forbidden("api.git.errors.push_foreign_ref",
				"allowed", strings.Join(allowed, ", "), "ref", c.Ref)
		}
		if strings.Trim(c.New, "0") == "" {
			return apperr.Forbidden("api.git.errors.push_delete_ref", "ref", c.Ref)
		}
	}
	return nil
}

func gitAuthorize(lookup func(ctx context.Context, rawKey string) (scopeTaskID string, ok bool), r *http.Request, repoPath string) bool {
	taskID, _, ok := strings.Cut(repoPath, "/")
	if !ok || taskID == "" {
		return false
	}
	_, pass, ok := r.BasicAuth()
	if !ok || pass == "" {
		return false
	}
	scope, ok := lookup(r.Context(), pass)
	return ok && scope == taskID
}

func gitResolveRepo(ctx context.Context, projectPaths func(context.Context, string) []string, repoPath string) (string, bool) {
	taskID, projectName, ok := strings.Cut(repoPath, "/")
	if !ok || taskID == "" || projectName == "" {
		return "", false
	}
	for _, p := range projectPaths(ctx, taskID) {
		if filepath.Base(p) == projectName {
			return p, true
		}
	}
	return "", false
}

func (s *Server) authorizeGitRequest(r *http.Request, repoPath string) bool {
	return gitAuthorize(s.lookupKeyScope, r, repoPath)
}

func (s *Server) lookupKeyScope(ctx context.Context, rawKey string) (string, bool) {
	info := s.authStore.LookupAPIKeyInfo(ctx, rawKey)
	if info == nil {
		return "", false
	}
	return info.Scope.TaskID, true
}

func (s *Server) resolveGitRepo(ctx context.Context, repoPath string) (string, bool) {
	dir, ok := gitResolveRepo(ctx, s.taskProjectPaths, repoPath)
	if ok {
		enableReceivePack(dir)
	}
	return dir, ok
}

func (s *Server) taskProjectPaths(ctx context.Context, taskID string) []string {
	t, err := s.taskStore.Get(ctx, taskID)
	if err != nil || t == nil {
		return nil
	}
	out := make([]string, 0, len(t.ProjectIDs))
	for _, pid := range t.ProjectIDs {
		if proj, perr := s.projectStore.Get(ctx, pid); perr == nil && proj != nil {
			out = append(out, proj.Path)
		}
	}
	return out
}

func enableReceivePack(dir string) {
	//nolint:gosec // G204: dir is a daemon-managed project path from projectStore, never user input.
	cmd := exec.CommandContext(context.Background(), "git", "-C", dir, "config", "http.receivepack", "true")
	if err := cmd.Run(); err != nil {
		//nolint:gosec // G706: dir sanitized with logx.Safe (strips control chars); gosec taint can't track custom sanitizers.
		log.Printf("[git] %s", i18n.Log("api.git.log.enable_receivepack_failed", "dir", logx.Safe(dir), "err", err))
	}
}
