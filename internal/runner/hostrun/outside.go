package hostrun

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// A run's TMPDIR is its own folder, but an agent can still write to the machine's /tmp by hand: one
// copied the client's repo twice to /tmp/rb3043 (3.7 GB) and left `ng serve` running from there.
// outsideWatch notes what /tmp and the machine's Docker hold when the run starts, and at its end
// removes what is provably the run's and reports the rest on the run.

// The most new /tmp entries one run reports by name; the rest are counted.
const outsideReportMax = 10

// How long a docker call may take before the runner stops asking it.
var dockerTimeout = 2 * time.Second

// The docker CLI the runner asks; tests point it at a fake.
var dockerCmd = "docker"

type outsideWatch struct {
	job string
	// tmpRoot resolved (/private/tmp on macOS).
	tmp    string
	before map[string]bool
	// The admin folders (<mirror>/.git/worktrees/<name>) of the run's own worktrees: a /tmp copy
	// whose .git file points at one is a copy of this run's code.
	gitdirs []string
	// Container ids when the run started; nil when docker did not answer.
	containers map[string]bool
	// The run's own folders, resolved: its folder and its task worktrees. A container created during
	// the run whose compose project or bind mount sits in one of them, or in a /tmp entry removed as
	// the run's, is the run's.
	own []string
}

// watchOutside notes the same-user entries of /tmp and the machine's containers before the run's
// script starts. root is the run's folder, wts its task worktrees.
func watchOutside(ctx context.Context, job, root string, wts []string) *outsideWatch {
	w := &outsideWatch{job: job, tmp: realPath(tmpRoot), before: map[string]bool{}}
	for _, dir := range append([]string{root}, wts...) {
		if dir != "" {
			w.own = append(w.own, realPath(dir))
		}
	}
	for _, name := range ownEntries(w.tmp) {
		w.before[name] = true
	}
	for _, wt := range wts {
		if gd := worktreeGitdir(wt); gd != "" {
			w.gitdirs = append(w.gitdirs, gd)
		}
	}
	if ids, ok := dockerContainers(ctx); ok {
		w.containers = map[string]bool{}
		for _, c := range ids {
			w.containers[c.id] = true
		}
	}
	return w
}

// ownEntries lists the top-level entries of dir that belong to this user.
func ownEntries(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	uid := os.Getuid()
	var out []string
	for _, e := range entries {
		if info, err := e.Info(); err == nil && ownedBy(info, uid) {
			out = append(out, e.Name())
		}
	}
	return out
}

func ownedBy(info fs.FileInfo, uid int) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

// worktreeGitdir reads the gitdir a worktree's .git file names, resolved; empty when it has none.
func worktreeGitdir(wt string) string {
	b, err := os.ReadFile(filepath.Join(wt, ".git")) //nolint:gosec // G304: a .git file under the runner's own folders or /tmp
	if err != nil {
		return ""
	}
	gd, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	gd = strings.TrimSpace(gd)
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(wt, gd)
	}
	return realPath(gd)
}

// finish reports and removes what the run left outside its folder. cwds are the working
// directories of the processes killRun stopped, by pid, and commands their command lines; with
// survivors left nothing is removed, as something of the run may still be using it.
func (w *outsideWatch) finish(ctx context.Context, cwds, commands map[int]string, survivors bool) []string {
	if w == nil {
		return nil
	}
	var left []string
	owned := slices.Clone(w.own)
	for _, path := range w.newEntries() {
		size, done := diskSize(path, sizeWalkLimit)
		if pid, gitdir, ok := w.runsEntry(path, cwds); ok {
			owned = append(owned, path)
			if !survivors && w.removeRunsEntry(path, bytesText(size, done), pid, gitdir, commands) {
				continue
			}
		}
		left = append(left, path+"\x00"+bytesText(size, done))
	}
	return append(w.reportLeft(left), w.finishContainers(ctx, owned)...)
}

// runsEntry tells whether a new /tmp entry is provably the run's: a killed process worked in it (pid),
// or it holds a copy of one of the run's worktrees (gitdir).
func (w *outsideWatch) runsEntry(path string, cwds map[int]string) (pid int, gitdir string, ok bool) {
	if pid, ok := cwdIn(path, cwds); ok {
		return pid, "", true
	}
	if gd := w.copyOf(path); gd != "" {
		return 0, gd, true
	}
	return 0, "", false
}

// removeRunsEntry removes an entry runsEntry tied to the run and tells whether it is gone; one that
// is not is reported on the run.
func (w *outsideWatch) removeRunsEntry(path, size string, pid int, gitdir string, commands map[int]string) bool {
	if !removeOutside(path) {
		return false
	}
	if gitdir == "" {
		log.Printf("[runner] %s", i18n.Log("hostrun.log.outside_removed_cwd", "job", w.job, "path", path,
			"size", size, "pid", pid, "command", commands[pid]))
	} else {
		log.Printf("[runner] %s", i18n.Log("hostrun.log.outside_removed_copy", "job", w.job, "path", path,
			"size", size, "gitdir", gitdir))
	}
	return true
}

// newEntries lists the same-user entries of /tmp that were not there when the run started, but for
// Claude Code's own folder (removeSession clears its part) and sockets and pipes, which are some
// other program's and hold no data.
func (w *outsideWatch) newEntries() []string {
	claudeOwn := "claude-" + strconv.Itoa(os.Getuid())
	var out []string
	for _, name := range ownEntries(w.tmp) {
		if w.before[name] || name == claudeOwn {
			continue
		}
		path := filepath.Join(w.tmp, name)
		if info, err := os.Lstat(path); err == nil && info.Mode()&(fs.ModeSocket|fs.ModeNamedPipe) == 0 {
			out = append(out, path)
		}
	}
	return out
}

// reportLeft turns the "<path>\x00<size>" entries the run left and the runner kept into warnings.
func (w *outsideWatch) reportLeft(left []string) []string {
	slices.Sort(left)
	var warnings []string
	for i, r := range left {
		if i == outsideReportMax {
			warnings = append(warnings, i18n.Text("hostrun.warnings.left_in_tmp_more", "count", len(left)-i, "dir", tmpRoot))
			break
		}
		path, size, _ := strings.Cut(r, "\x00")
		warnings = append(warnings, i18n.Text("hostrun.warnings.left_in_tmp", "dir", tmpRoot, "path", path, "size", size))
		log.Printf("[runner] %s", i18n.Log("hostrun.log.outside_left", "job", w.job, "path", path, "size", size))
	}
	return warnings
}

// removeOutside removes path and tells whether it is gone. RemoveAll removes a symlink, never what
// it points at: a copy's node_modules links stay links.
func removeOutside(path string) bool {
	if err := os.RemoveAll(path); err != nil {
		logx.Warn(err, "hostrun.log.remove_failed", "path", path)
		return false
	}
	return true
}

// cwdIn tells whether one of the killed processes worked in path or under it.
func cwdIn(path string, cwds map[int]string) (int, bool) {
	pids := make([]int, 0, len(cwds))
	for pid := range cwds {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	for _, pid := range pids {
		cwd := realPath(cwds[pid])
		if cwd == path || strings.HasPrefix(cwd, path+string(filepath.Separator)) {
			return pid, true
		}
	}
	return 0, false
}

// How deep under a new /tmp entry the runner looks for the .git file of a copied worktree:
// /tmp/rb3043/after/.git is two levels down.
const copyDepth = 3

// copyOf returns the worktree admin folder a .git file under path points at when it is one of the
// run's own worktrees. It never runs git there: `git worktree remove` or prune from a copy would
// unregister the live worktree.
func (w *outsideWatch) copyOf(path string) string {
	if len(w.gitdirs) == 0 {
		return ""
	}
	found := ""
	// The walk's error is only ever the SkipAll that ends it.
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // see above
		if err != nil {
			return nil //nolint:nilerr // an unreadable folder is skipped, the rest is still looked at
		}
		depth := strings.Count(strings.TrimPrefix(p, path), string(filepath.Separator))
		if d.IsDir() && (depth >= copyDepth || d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.Name() != ".git" || !d.Type().IsRegular() {
			return nil
		}
		if gd := worktreeGitdir(filepath.Dir(p)); slices.Contains(w.gitdirs, gd) {
			found = gd
			return fs.SkipAll
		}
		return nil
	})
	return found
}

type dockerContainer struct {
	id, name, state string
}

// dockerContainers lists every container of the machine's Docker; false when docker is not there
// or does not answer within dockerTimeout.
func dockerContainers(ctx context.Context) ([]dockerContainer, bool) {
	if _, err := exec.LookPath(dockerCmd); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	format := "{{.ID}}\t{{.Names}}\t{{.State}}"
	cmd := exec.CommandContext(ctx, dockerCmd, "ps", "-a", "--no-trunc", "--format", format)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var list []dockerContainer
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) == 3 && f[0] != "" {
			list = append(list, dockerContainer{id: f[0], name: f[1], state: f[2]})
		}
	}
	return list, true
}

// finishContainers removes the containers created while the run ran that are provably the run's
// (containerOwners) and reports the others: they are the machine's Docker's, and a person may have
// started one meanwhile. owned are the run's folders and the /tmp entries that were the run's.
func (w *outsideWatch) finishContainers(ctx context.Context, owned []string) []string {
	if w.containers == nil {
		return nil
	}
	now, ok := dockerContainers(ctx)
	if !ok {
		return nil
	}
	var fresh []dockerContainer
	for _, c := range now {
		if !w.containers[c.id] {
			fresh = append(fresh, c)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	proofs := containerOwners(ctx, fresh, owned)
	var warnings []string
	for _, c := range fresh {
		if proof := proofs[c.id]; proof != "" && removeContainer(ctx, c.id) {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.container_removed", "job", w.job, "name", c.name,
				"state", c.state, "path", proof))
			continue
		}
		key := "hostrun.warnings.container_left"
		if c.state == "running" {
			key = "hostrun.warnings.container_running"
		}
		warnings = append(warnings, i18n.Text(key, "name", c.name, "state", c.state))
		log.Printf("[runner] %s", i18n.Log("hostrun.log.container_left", "job", w.job, "name", c.name, "state", c.state))
	}
	return warnings
}

// How long docker inspect, and each docker rm, may take.
var dockerChangeTimeout = 10 * time.Second

// containerOwners returns, by container id, the folder that makes a container the run's: the
// working directory of its compose project, or the source of one of its bind mounts, inside owned.
// Containers it cannot tie to the run are not in the map.
func containerOwners(ctx context.Context, list []dockerContainer, owned []string) map[string]string {
	out := map[string]string{}
	if len(owned) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, dockerChangeTimeout)
	defer cancel()
	args := []string{"inspect", "--format", "{{.Id}}\t{{json .Config.Labels}}\t{{json .Mounts}}"}
	for _, c := range list {
		args = append(args, c.id)
	}
	cmd := exec.CommandContext(ctx, dockerCmd, args...)
	cmd.WaitDelay = time.Second
	// A container removed meanwhile makes inspect exit 1; what it printed holds for the rest.
	b, _ := cmd.Output() //nolint:errcheck // see above
	for line := range strings.SplitSeq(string(b), "\n") {
		id, path := parseContainerOwner(line, owned)
		if id != "" && path != "" {
			out[id] = path
		}
	}
	return out
}

// parseContainerOwner reads one line of containerOwners' inspect and returns the container's id and
// the path inside owned that ties it to the run, empty when none does.
func parseContainerOwner(line string, owned []string) (id, path string) {
	f := strings.SplitN(strings.TrimSpace(line), "\t", 3)
	if len(f) != 3 || f[0] == "" {
		return "", ""
	}
	var labels map[string]string
	var mounts []struct {
		Type, Source string
	}
	_ = json.Unmarshal([]byte(f[1]), &labels) //nolint:errcheck // a container without labels prints null
	_ = json.Unmarshal([]byte(f[2]), &mounts) //nolint:errcheck // nor mounts
	candidates := []string{labels["com.docker.compose.project.working_dir"]}
	for _, m := range mounts {
		if m.Type == "bind" {
			candidates = append(candidates, m.Source)
		}
	}
	for _, c := range candidates {
		if c == "" || !filepath.IsAbs(c) {
			continue
		}
		p := resolvedPath(c)
		for _, dir := range owned {
			if p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) {
				return f[0], c
			}
		}
	}
	return f[0], ""
}

// resolvedPath is p with its symlinks resolved as far as it exists: a bind mount's source may be a
// folder the run already removed.
func resolvedPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolvedPath(parent), filepath.Base(p))
}

// removeContainer removes a container of the run, running or not, and tells whether it is gone.
func removeContainer(ctx context.Context, id string) bool {
	ctx, cancel := context.WithTimeout(ctx, dockerChangeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, dockerCmd, "rm", "-f", "-v", id) //nolint:gosec // G204: the docker CLI with an id it listed
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		logx.Warn(apperr.Wrap(err, "hostrun.errors.container_remove", "id", id, "out", strings.TrimSpace(string(out))),
			"hostrun.log.container_remove_failed", "id", id)
		return false
	}
	return true
}
