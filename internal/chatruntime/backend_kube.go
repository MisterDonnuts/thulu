//go:build !foss

package chatruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/sandbox/execagent"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Must match the sandbox execagent and the EXPOSE in images/kube-agent/Dockerfile.
const chatExecPort = 8587

// Generous because a cold node may pull the agent image first.
const chatPodStartTimeout = 120 * time.Second

// Turns go over HTTP to the in-pod exec agent, not pods/exec: the daemon's ClusterRole withholds
// that verb.
type kubeBackend struct {
	rt         *kuberuntime.Runtime
	httpc      *http.Client
	gitBaseURL string

	mu     sync.Mutex
	tokens map[string]string
}

func newKubeBackend(rt *kuberuntime.Runtime, gitBaseURL string) *kubeBackend {
	return &kubeBackend{
		rt:         rt,
		gitBaseURL: strings.TrimRight(gitBaseURL, "/"),
		// Must outlast a full LLM turn; matches the sandbox client.
		httpc:  &http.Client{Timeout: 15 * time.Minute},
		tokens: map[string]string{},
	}
}

func (b *kubeBackend) Available() bool { return b.rt != nil }

func (b *kubeBackend) Create(ctx context.Context, spec CreateSpec) (string, error) {
	c, at := spec.Chat, spec.AgentType

	image := ""
	if s, _, perr := types.ParseComposeSpec(at.Dir); perr == nil {
		image = s.Image
	}

	workspaceID := chatWorkspaceID(c.WorkspaceID)
	ns := b.rt.NamespaceFor(workspaceID)
	if err := b.rt.EnsureTenantNamespace(ctx, workspaceID); err != nil {
		return "", apperr.Wrap(err, "chatruntime.errors.kube_ensure_namespace")
	}

	token, err := mintExecToken()
	if err != nil {
		return "", err
	}
	// The exec agent refuses to start without OCTOPUS_EXEC_TOKEN, and its presence makes the chat
	// entrypoint
	// run the agent instead of parking (images/entrypoint-base.sh chat mode).
	env := spec.Env
	env["OCTOPUS_EXEC_TOKEN"] = token
	env["OCTOPUS_EXEC_ADDR"] = fmt.Sprintf(":%d", chatExecPort)

	podName, err := b.rt.LaunchPod(ctx, kuberuntime.LaunchSpec{
		WorkspaceID: workspaceID,
		TaskID:      c.ID,
		Stage:       spec.Stage,
		InstanceID:  c.ID,
		Image:       image,
		Engine:      env["ENGINE"],
		Env:         env,
		Worktrees:   b.codeMounts(c, spec.Code, env),
		Annotations: map[string]string{
			kuberuntime.AnnotationInstanceID: c.ID,
			kuberuntime.AnnotationAgentType:  at.Name,
		},
		Browser: browserrun.Startable(env),
	})
	if err != nil {
		return "", err
	}
	// Store the token before anything can try a turn.
	b.mu.Lock()
	b.tokens[podName] = token
	b.mu.Unlock()

	if err := b.rt.EnsureChatExecService(ctx, podName, ns, chatExecPort); err != nil {
		b.teardown(ctx, podName, ns)
		return "", err
	}
	if err := b.waitHealthy(ctx, podName, ns); err != nil {
		b.teardown(ctx, podName, ns)
		return "", err
	}
	return podName, nil
}

// The git remote serves /git/<task>/<project> only to a key scoped to that task
// (Manager.applyRunKey);
// a chat with no task or no key gets no code.
func (b *kubeBackend) codeMounts(c *chat.Chat, code map[string]types.CodeView, env map[string]string) []kuberuntime.WorktreeMount {
	if b.gitBaseURL == "" || c.TaskID == "" || env[runKeyEnv] == "" || len(code) == 0 {
		return nil
	}
	names := make([]string, 0, len(code))
	for name := range code {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]kuberuntime.WorktreeMount, 0, len(names))
	for _, name := range names {
		out = append(out, kuberuntime.WorktreeMount{
			Name:          name,
			CloneURL:      fmt.Sprintf("%s/%s/%s", b.gitBaseURL, c.TaskID, name),
			Branch:        code[name].Branch,
			ReadOnly:      true,
			CredentialEnv: runKeyEnv,
		})
	}
	return out
}

func (b *kubeBackend) Exec(ctx context.Context, spec ExecSpec) (ExecOutput, error) {
	ns, err := b.rt.FindPodNamespace(ctx, spec.ContainerID)
	if err != nil {
		return ExecOutput{}, apperr.Wrap(err, "chatruntime.errors.kube_locate_pod", "pod", spec.ContainerID)
	}
	b.mu.Lock()
	token := b.tokens[spec.ContainerID]
	b.mu.Unlock()
	if token == "" {
		// Tokens live only in memory; after a daemon restart the pod cannot be driven, so recreate
		// it.
		b.teardown(ctx, spec.ContainerID, ns)
		return ExecOutput{}, errChatPodNeedsRecreate
	}

	url := fmt.Sprintf("http://%s.%s.svc:%d", spec.ContainerID, ns, chatExecPort)
	res, err := execagent.Exec(ctx, b.httpc, url, token, execagent.ExecRequest{
		Command:    []string{"/workspace/snippets/chat-turn.sh"},
		Env:        kvSliceToMap(spec.Env),
		Stdin:      spec.Stdin,
		InheritEnv: true,
	})
	if err != nil {
		return ExecOutput{}, err
	}
	out := ExecOutput{Stdout: []byte(res.Stdout), Stderr: []byte(res.Stderr), ExitCode: res.ExitCode}
	if res.TimedOut {
		return out, apperr.Timeout("chatruntime.errors.kube_turn_timeout")
	}
	return out, nil
}

func (b *kubeBackend) Running(ctx context.Context, podName string) bool {
	if podName == "" {
		return false
	}
	ns, err := b.rt.FindPodNamespace(ctx, podName)
	if err != nil {
		return false
	}
	return b.rt.PodAlive(ctx, podName, ns)
}

func (b *kubeBackend) Remove(ctx context.Context, podName string) {
	if podName == "" {
		return
	}
	ns, err := b.rt.FindPodNamespace(ctx, podName)
	if err != nil {
		b.mu.Lock()
		delete(b.tokens, podName)
		b.mu.Unlock()
		return
	}
	b.teardown(ctx, podName, ns)
}

func (b *kubeBackend) ListManaged(ctx context.Context) ([]string, error) {
	refs, err := b.rt.ListChatPods(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out, nil
}

func (b *kubeBackend) teardown(ctx context.Context, podName, ns string) {
	// Owner-reference GC would remove it, but an explicit delete frees the name at once for a
	// recreate.
	if err := b.rt.DeleteService(ctx, podName, ns); err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.delete_exec_service_failed", "ns", ns, "pod", podName, "err", err))
	}
	if err := b.rt.DeletePod(ctx, podName, ns); err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.delete_pod_failed", "ns", ns, "pod", podName, "err", err))
	}
	b.mu.Lock()
	delete(b.tokens, podName)
	b.mu.Unlock()
}

func (b *kubeBackend) waitHealthy(ctx context.Context, podName, ns string) error {
	ctx, cancel := context.WithTimeout(ctx, chatPodStartTimeout)
	defer cancel()
	url := fmt.Sprintf("http://%s.%s.svc:%d/healthz", podName, ns, chatExecPort)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if exit, done := b.rt.PodExited(ctx, podName, ns); done {
			return b.exitedErr(ctx, podName, ns, exit)
		}
		if b.healthy(ctx, url) {
			return nil
		}
		select {
		case <-ctx.Done():
			return apperr.WrapKind(apperr.KindUnavailable, ctx.Err(), "chatruntime.errors.kube_exec_agent_unhealthy",
				"pod", podName, "timeout", chatPodStartTimeout)
		case <-ticker.C:
		}
	}
}

const (
	chatPodLogTail = 20
	chatPodLogMax  = 2000
)

func (b *kubeBackend) exitedErr(ctx context.Context, podName, ns string, exit kuberuntime.PodExit) error {
	if exit.Container == "" {
		return apperr.Unavailable("chatruntime.errors.kube_chat_pod_gone", "pod", podName, "reason", exit.Reason)
	}
	return apperr.Unavailable("chatruntime.errors.kube_chat_pod_exited", "pod", podName,
		"container", exit.Container, "code", exit.ExitCode, "reason", exit.Reason,
		"log", b.logTail(ctx, podName, ns, exit.Container))
}

func (b *kubeBackend) logTail(ctx context.Context, podName, ns, container string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stream, err := b.rt.ContainerLogsTail(ctx, podName, ns, container, chatPodLogTail)
	if err != nil {
		return "-"
	}
	defer func() { _ = stream.Close() }()
	buf, err := io.ReadAll(io.LimitReader(stream, 64<<10))
	if err != nil && len(buf) == 0 {
		return "-"
	}
	out := strings.TrimSpace(string(buf))
	if len(out) > chatPodLogMax {
		out = strings.ToValidUTF8(out[len(out)-chatPodLogMax:], "")
	}
	if out == "" {
		return "-"
	}
	return out
}

func (b *kubeBackend) healthy(ctx context.Context, url string) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false
	}
	resp, err := b.httpc.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func chatWorkspaceID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "default"
	}
	return id
}

func mintExecToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.Wrap(err, "chatruntime.errors.kube_mint_exec_token")
	}
	return hex.EncodeToString(buf), nil
}

func kvSliceToMap(kv []string) map[string]string {
	out := make(map[string]string, len(kv))
	for _, e := range kv {
		if k, v, ok := strings.Cut(e, "="); ok {
			out[k] = v
		}
	}
	return out
}
