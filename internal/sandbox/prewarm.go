package sandbox

// The source reaches the sandbox as a plain tree with no .git, so the project's token never leaves
// the daemon.
// Cloning through the daemon's git proxy instead would need a credential in the pod and a route
// that also accepts pushes.

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
)

const PrewarmStage = "prewarm"

const prewarmOwnerPrefix = "prewarm:"

// The queue's concurrency policy and the janitors recognize prewarm owners by this prefix.
func PrewarmOwner(projectID string) string { return prewarmOwnerPrefix + projectID }

func PrewarmProjectID(owner string) (string, bool) {
	id, ok := strings.CutPrefix(owner, prewarmOwnerPrefix)
	return id, ok && id != ""
}

var ErrPrewarmQueued error = apperr.Unavailable("sandbox.errors.prewarm_queued")

var (
	prewarmPollEvery   = 5 * time.Second
	prewarmMaxDuration = (initSetupMaxSeconds + 15*60) * time.Second
)

const (
	// Base64 of 512 KiB (~700 KiB) stays under the Kubernetes exec agent's 1 MiB request limit with
	// room for the JSON around it.
	prewarmChunk      = 512 << 10
	prewarmMaxArchive = 2 << 30
)

type PrewarmSpec struct {
	WorkspaceID string
	ProjectID   string
	Name        string
	Dir         string
	Manifest    project.InitManifest
	HostPath    string
	Archive     func() (io.ReadCloser, error)
}

type PrewarmResult struct {
	Key              string
	SizeBytes        int64
	Pruned           []string
	Reused           bool
	ToolsUnavailable bool
	ToolsMs          int64
	SetupMs          int64
	ToolsDownloaded  []string
	ToolsUsed        []string
	ToolsFailed      bool
}

// Must match the return codes of octopus_prewarm in initSetupLib (setup.go).
const (
	prewarmExitNoCache   = 10
	prewarmExitReadOnly  = 11
	prewarmExitNoZstd    = 12
	prewarmExitTools     = 13
	prewarmExitSetup     = 14
	prewarmExitNoPaths   = 15
	prewarmExitTar       = 16
	prewarmExitNoWorkdir = 3
)

func prewarmDir() string { return initRoot + "/prewarm" }

func (m *Manager) Prewarm(ctx context.Context, spec PrewarmSpec) (PrewarmResult, error) {
	if !spec.Manifest.HasSetupSnapshot() {
		return PrewarmResult{}, apperr.Invalid("sandbox.errors.prewarm_not_eligible")
	}
	if (spec.HostPath == "") == (spec.Archive == nil) {
		return PrewarmResult{}, apperr.Internal("sandbox.errors.prewarm_source")
	}
	owner := PrewarmOwner(spec.ProjectID)
	opts := AcquireDockerOpts{WorkspaceID: spec.WorkspaceID, SnapshotsWritable: true}
	if spec.HostPath != "" {
		opts.ProjectsPaths = map[string]string{spec.Dir: spec.HostPath}
	}
	// Always start fresh: a sandbox left over by a crash holds another commit.
	logx.Warn(m.Destroy(ctx, owner), "sandbox.log.prewarm_teardown", "project", spec.ProjectID)
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		logx.Warn(m.Destroy(dctx, owner), "sandbox.log.prewarm_teardown", "project", spec.ProjectID)
	}()

	t := &initTarget{owner: owner, stage: PrewarmStage, opts: opts, proj: InitProject{ID: spec.ProjectID, Name: spec.Name, Dir: spec.Dir}}
	res, err := m.prewarm(ctx, t, spec)
	if errors.Is(err, errInitQueued) {
		logx.Warn(m.queue.Cancel(context.WithoutCancel(ctx), owner), "sandbox.log.cancel_queue_entry", "task", owner)
		return PrewarmResult{}, ErrPrewarmQueued
	}
	m.measurePrewarmCache(ctx, spec.WorkspaceID, owner)
	return res, err
}

func (m *Manager) prewarm(ctx context.Context, t *initTarget, spec PrewarmSpec) (PrewarmResult, error) {
	d := prewarmDir()
	repo := path.Join(initProjectsRoot, spec.Dir)
	workdir := path.Join(repo, spec.Manifest.Workdir)
	qd := shellSingleQuote(d)

	//i18n:ignore shell command run in the sandbox
	if err := m.prewarmStep(ctx, t, fmt.Sprintf(`rm -rf %[1]s && mkdir -p %[1]s`, qd), ""); err != nil {
		return PrewarmResult{}, err
	}
	if spec.Archive != nil {
		if err := m.prewarmSendSource(ctx, t, spec.Archive, d, repo); err != nil {
			return PrewarmResult{}, err
		}
	}

	files := []struct{ name, data string }{
		{"env", initEnvFile(map[string]string{"OCTOPUS_PROJECT": spec.Name, InitFilesEnv: initFilesPath(d)})},
		{"prepare.sh", setupPrepareScript(setupSpec{dir: d, repo: repo, projectID: spec.ProjectID, manifest: spec.Manifest})},
		{"setup.sh", spec.Manifest.Setup},
		{"prewarm.sh", prewarmScript(d)},
	}
	for _, f := range files {
		if err := m.writeInitFile(ctx, t, d+"/"+f.name, f.data); err != nil {
			return PrewarmResult{}, err
		}
	}
	if err := m.writeInitSupportFiles(ctx, t, d, spec.Manifest.Files); err != nil {
		return PrewarmResult{}, err
	}
	//i18n:ignore shell command run in the sandbox
	launch := fmt.Sprintf(`d=%s
command -v setsid >/dev/null 2>&1 || exit %d
cd %s 2>/dev/null || exit %d
setsid nohup bash "$d/prewarm.sh" > "$d/prewarm.stderr" 2>&1 < /dev/null &
true`, qd, initExitNoSetsid, shellSingleQuote(workdir), prewarmExitNoWorkdir)
	lres, _, err := m.initExec(ctx, t, launch, initStepTimeoutMs)
	if err != nil {
		return PrewarmResult{}, err
	}
	switch lres.ExitCode {
	case 0:
	case prewarmExitNoWorkdir:
		return PrewarmResult{}, apperr.Invalid("sandbox.errors.init_workdir_missing", "workdir", workdir)
	case initExitNoSetsid:
		return PrewarmResult{}, apperr.Unavailable("sandbox.errors.init_no_setsid")
	default:
		return PrewarmResult{}, apperr.Internal("sandbox.errors.init_start_failed", "stderr", strings.TrimSpace(lres.Stderr))
	}

	code, err := m.prewarmWait(ctx, t, d)
	if err != nil {
		return PrewarmResult{}, err
	}
	//i18n:ignore shell command run in the sandbox
	out, _, err := m.initExec(ctx, t, fmt.Sprintf(`cat %s/prewarm.out 2>/dev/null`, qd), initProbeTimeoutMs)
	if err != nil {
		return PrewarmResult{}, err
	}
	res := parsePrewarmOut(out.Stdout)
	if code == 0 && res.Key != "" {
		return res, nil
	}
	//i18n:ignore shell command run in the sandbox
	tailCmd := fmt.Sprintf(`tail -c 3000 %[1]s/prewarm.log 2>/dev/null; tail -c 1000 %[1]s/prewarm.stderr 2>/dev/null`, qd)
	tail, _, tailErr := m.initExec(ctx, t, tailCmd, initProbeTimeoutMs)
	logTail := ""
	if tailErr == nil && tail != nil {
		logTail = strings.TrimSpace(tail.Stdout)
	}
	res.ToolsFailed = code == prewarmExitTools
	return res, prewarmExitError(code, logTail, spec.Manifest.Cache.Paths)
}

func (m *Manager) prewarmStep(ctx context.Context, t *initTarget, cmd, stdin string) error {
	res, _, err := m.Exec(ctx, t.owner, t.stage, t.opts, ExecOpts{Command: cmd, Stdin: stdin, TimeoutMs: initStepTimeoutMs})
	if err != nil {
		return err
	}
	if res == nil {
		return errInitQueued
	}
	if res.ExitCode != 0 {
		return apperr.Internal("sandbox.errors.prewarm_step", "stderr", strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (m *Manager) prewarmSendSource(ctx context.Context, t *initTarget, archive func() (io.ReadCloser, error), d, repo string) error {
	src, openErr := archive()
	if openErr != nil {
		return openErr
	}
	defer src.Close()
	tgz := shellSingleQuote(d + "/src.tgz")
	buf := make([]byte, prewarmChunk)
	var total int64
	for {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			total += int64(n)
			if total > prewarmMaxArchive {
				return apperr.TooLarge("sandbox.errors.prewarm_source_too_large", "max", prewarmMaxArchive>>20)
			}
			//i18n:ignore shell command run in the sandbox
			if err := m.prewarmStep(ctx, t, "base64 -d >> "+tgz, base64.StdEncoding.EncodeToString(buf[:n])); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			break
		}
		if rerr != nil {
			return apperr.Wrap(rerr, "sandbox.errors.prewarm_archive")
		}
	}
	// Close reports git archive's exit, i.e. whether the whole tree was produced; check it before
	// unpacking.
	if err := src.Close(); err != nil {
		return err
	}
	//i18n:ignore shell command run in the sandbox
	return m.prewarmStep(ctx, t, fmt.Sprintf(`mkdir -p %[1]s && tar xzf %[2]s -C %[1]s && rm -f %[2]s`, shellSingleQuote(repo), tgz), "")
}

func prewarmScript(d string) string {
	qd := shellSingleQuote(d)
	//i18n:ignore shell script run in the sandbox
	return fmt.Sprintf(`echo $$ > %[1]s/pid
. %[1]s/env
. %[1]s/prepare.sh
octopus_prewarm > %[1]s/prewarm.out
echo $? > %[1]s/prewarm.exit
`, qd)
}

func (m *Manager) prewarmWait(ctx context.Context, t *initTarget, d string) (int, error) {
	//i18n:ignore shell command run in the sandbox
	probe := initAliveFn + "\n" + fmt.Sprintf(`d=%s
echo "exit=$(cat "$d/prewarm.exit" 2>/dev/null)"
pid=$(cat "$d/pid" 2>/dev/null)
a=0; if [ -n "$pid" ] && alive "$pid"; then a=1; fi
echo "alive=$a"`, shellSingleQuote(d))
	deadline := time.Now().Add(prewarmMaxDuration)
	gone := 0
	for {
		res, _, err := m.initExec(ctx, t, probe, initProbeTimeoutMs)
		if err != nil {
			return 0, err
		}
		p := parseInitProbe(res.Stdout)
		switch {
		case p.exited:
			return p.exitCode, nil
		case p.answered && !p.alive:
			// The exit file is written just after the process ends; allow one more round before
			// calling the run lost.
			gone++
			if gone > 1 {
				return 0, apperr.Internal("sandbox.errors.prewarm_lost")
			}
		}
		if time.Now().After(deadline) {
			_, _, stopErr := m.initExec(ctx, t, initStopCmd("prewarm"), initStepTimeoutMs)
			logx.Warn(stopErr, "sandbox.log.prewarm_teardown", "project", t.proj.ID)
			return 0, apperr.Unavailable("sandbox.errors.prewarm_timeout", "minutes", int(prewarmMaxDuration/time.Minute))
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(prewarmPollEvery):
		}
	}
}

func parsePrewarmOut(out string) PrewarmResult {
	res := PrewarmResult{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "prewarm_key":
			res.Key = v
		case "prewarm_size":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				res.SizeBytes = n
			}
		case "prewarm_pruned":
			res.Pruned = append(res.Pruned, v)
		case "prewarm_reused":
			res.Reused = v == "1"
		case "prewarm_tools":
			res.ToolsUnavailable = v == "unavailable"
		default:
			res.parseTimings(k, v)
		}
	}
	return res
}

func (res *PrewarmResult) parseTimings(k, v string) {
	n, nErr := strconv.ParseInt(v, 10, 64)
	switch k {
	case "prewarm_tools_ms":
		if nErr == nil {
			res.ToolsMs = n
		}
	case "prewarm_setup_ms":
		if nErr == nil {
			res.SetupMs = n
		}
	case "prewarm_tools_downloaded":
		res.ToolsDownloaded = splitToolList(v)
	case "prewarm_tools_used":
		res.ToolsUsed = splitToolList(v)
	}
}

func prewarmExitError(code int, logTail string, paths []string) error {
	switch code {
	case prewarmExitNoCache:
		return apperr.Unavailable("sandbox.errors.prewarm_no_cache")
	case prewarmExitReadOnly:
		return apperr.Unavailable("sandbox.errors.prewarm_snapshots_readonly")
	case prewarmExitNoZstd:
		return apperr.Unavailable("sandbox.errors.prewarm_no_zstd")
	case prewarmExitTools:
		return apperr.Invalid("sandbox.errors.prewarm_tools_failed", "log", logTail)
	case prewarmExitSetup:
		return apperr.Invalid("sandbox.errors.prewarm_setup_failed", "log", logTail)
	case prewarmExitNoPaths:
		return apperr.Invalid("sandbox.errors.prewarm_no_paths", "paths", strings.Join(paths, ", "))
	case prewarmExitTar:
		return apperr.Internal("sandbox.errors.prewarm_tar_failed", "log", logTail)
	}
	log.Printf("[sandbox] %s", i18n.Log("sandbox.log.prewarm_exit", "code", code))
	return apperr.Internal("sandbox.errors.prewarm_failed", "code", code, "log", logTail)
}
