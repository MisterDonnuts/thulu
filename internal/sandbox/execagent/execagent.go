// Deliberately not pods/exec: the daemon's ClusterRole withholds that verb (deploy/helm rbac.yaml).
// Commands are untrusted tenant code; isolation is the Kata microVM, the bearer token only gates
// who drives the pod.
package execagent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type ExecRequest struct {
	Command    []string          `json:"command"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	TimeoutMs  int               `json:"timeout_ms,omitempty"`
	Stdin      string            `json:"stdin,omitempty"`
	InheritEnv bool              `json:"inherit_env,omitempty"`
}

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

const defaultTimeout = 10 * time.Minute

type Server struct {
	token   string
	maxOut  int
	runFunc func(ctx context.Context, req ExecRequest) ExecResult
}

func NewServer(token string) *Server {
	s := &Server{token: token, maxOut: 10 << 20}
	s.runFunc = s.runCommand
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apperr.WriteJSON(w, r, http.StatusMethodNotAllowed, apperr.New("execagent.errors.method_not_allowed"))
		return
	}
	if !s.authorized(r) {
		apperr.WriteJSON(w, r, 0, apperr.Unauthenticated("execagent.errors.unauthorized"))
		return
	}
	var req ExecRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, 0, apperr.WrapKind(apperr.KindInvalid, err, "execagent.errors.bad_request"))
		return
	}
	if len(req.Command) == 0 {
		apperr.WriteJSON(w, r, 0, apperr.Invalid("execagent.errors.command_required"))
		return
	}
	res := s.runFunc(r.Context(), req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer " //i18n:ignore HTTP auth scheme
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) {
		return false
	}
	got := h[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) runCommand(ctx context.Context, req ExecRequest) ExecResult {
	timeout := defaultTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	//nolint:gosec // G204: running arbitrary tenant commands is this server's entire purpose; isolation is the Kata microVM around the pod.
	cmd := exec.CommandContext(ctx, req.Command[0], req.Command[1:]...)
	ownProcessGroup(cmd)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	if req.InheritEnv || len(req.Env) > 0 {
		var env []string
		if req.InheritEnv {
			env = os.Environ()
		}
		for k, v := range req.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, n: s.maxOut}
	cmd.Stderr = &limitedWriter{w: &stderr, n: s.maxOut}

	err := cmd.Run()
	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
			res.Stderr += "\n" + err.Error()
		}
	}
	return res
}

func Exec(ctx context.Context, httpc *http.Client, baseURL, token string, req ExecRequest) (*ExecResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, apperr.Wrap(err, "execagent.errors.marshal_request")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/exec", bytes.NewReader(body))
	if err != nil {
		return nil, apperr.Wrap(err, "execagent.errors.build_request")
	}
	httpReq.Header.Set("Authorization", "Bearer "+token) //i18n:ignore HTTP auth scheme
	httpReq.Header.Set("Content-Type", "application/json")
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(httpReq)
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindUnavailable, err, "execagent.errors.call_sandbox")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, rerr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if rerr != nil {
			msg = nil
		}
		return nil, apperr.New("execagent.errors.sandbox_status", "status", resp.StatusCode, "body", errorBody(msg))
	}
	var res ExecResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, apperr.Wrap(err, "execagent.errors.decode_result")
	}
	return &res, nil
}

func errorBody(msg []byte) string {
	var b apperr.Body
	if json.Unmarshal(msg, &b) == nil && b.Error != "" {
		return b.Error
	}
	return string(bytes.TrimSpace(msg))
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil // Report success past the cap so the command keeps running instead of failing on a short write.
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	written, err := l.w.Write(p)
	l.n -= written
	return len(p), err
}
