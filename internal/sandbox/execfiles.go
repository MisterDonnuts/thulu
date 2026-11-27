package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// ExecFile is a file Thulu keeps in the sandbox for a command: written before the command runs
// whenever its content changed, and named in Env for the command. The project's gate profile
// reaches thulu-gates this way ($THULU_GATES_PROFILE).
type ExecFile struct {
	Path    string
	Content string
	Env     string
}

// SandboxGatesProfilePath is where the task's gate profile is written in the sandbox: outside every
// repo and outside the /tmp/thulu-gates folders a run cleans up.
const SandboxGatesProfilePath = "/tmp/.thulu/gates-profile.json"

const execFileTimeoutMs = 20_000

// execFiles remembers what each container already holds, so an exec writes a file only when it is
// new there or changed: one extra command per container and change, not one per exec. The manager
// forgets a container when it creates or removes it: a Kubernetes sandbox pod is named after its
// task, so the one that replaces it has the same id and an empty /tmp.
type execFiles struct {
	mu      sync.Mutex
	written map[string]string
}

func (f *execFiles) has(container, p, sum string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written[container+"\x00"+p] == sum
}

func (f *execFiles) forget(container string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := container + "\x00"
	for k := range f.written {
		if strings.HasPrefix(k, prefix) {
			delete(f.written, k)
		}
	}
}

func (f *execFiles) mark(container, p, sum string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.written == nil {
		f.written = map[string]string{}
	}
	f.written[container+"\x00"+p] = sum
}

// placeFiles writes exec's files into the container and names each in the command's environment.
// A file that cannot be written is logged and left out of the environment: the command still runs,
// and thulu-gates without its profile is only stricter.
func (m *Manager) placeFiles(ctx context.Context, container string, exec *ExecOpts) {
	for _, file := range exec.Files {
		if err := m.placeFile(ctx, container, file); err != nil {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.exec_file_failed", "path", file.Path, "container", container, "err", err))
			continue
		}
		if file.Env == "" {
			continue
		}
		if exec.Env == nil {
			exec.Env = map[string]string{}
		}
		exec.Env[file.Env] = file.Path
	}
}

func (m *Manager) placeFile(ctx context.Context, container string, file ExecFile) error {
	sum := sha256.Sum256([]byte(file.Content))
	key := hex.EncodeToString(sum[:])
	if m.files.has(container, file.Path, key) {
		return nil
	}
	p := shellSingleQuote(file.Path)
	//i18n:ignore shell command run in the sandbox
	cmd := fmt.Sprintf(`mkdir -p %s && cat > %s.tmp && mv %s.tmp %s`, shellSingleQuote(path.Dir(file.Path)), p, p, p)
	res, err := m.docker.Exec(ctx, container, ExecOpts{Command: cmd, Stdin: file.Content, TimeoutMs: execFileTimeoutMs})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 || res.TimedOut {
		return apperr.Unavailable("sandbox.errors.exec_file_write", "path", file.Path, "code", res.ExitCode, "stderr", res.Stderr)
	}
	m.files.mark(container, file.Path, key)
	return nil
}
