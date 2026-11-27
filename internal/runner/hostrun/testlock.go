package hostrun

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// Locked tests: the daemon has no copy of a machine's code, so the job names what to read
// (JobSpec.TestLock) and the run's end sends the git blobs back (FinishRequest.TestFiles). The
// daemon decides what is locked and what changed; the runner only reads its tree.

// recordStarts keeps the heads a lock_tests run starts from, so its end can tell what it changed.
func (j *job) recordStarts(ctx context.Context) {
	tl := j.spec.TestLock
	if tl == nil || len(tl.Lock) == 0 || j.spec.ReadOnly {
		return
	}
	j.starts = map[string]string{}
	for _, pid := range tl.Lock {
		wt := j.wts[pid]
		if wt == "" {
			continue
		}
		head, err := git(ctx, wt, "rev-parse", "HEAD")
		if err != nil {
			logx.Warn(err, "hostrun.log.test_files_failed", "project", pid, "job", j.spec.ID)
			continue
		}
		j.starts[pid] = head
	}
}

// testFiles reads what the job's TestLock asks, after the run's work is committed. A project whose
// tree could not be read is left out, and the daemon says its lock went unchecked.
func (j *job) testFiles(ctx context.Context) []runner.TestFiles {
	tl := j.spec.TestLock
	if tl == nil || j.spec.ReadOnly {
		return nil
	}
	var out []runner.TestFiles
	for i := range j.spec.Projects {
		p := &j.spec.Projects[i]
		check, lock := tl.Check[p.ID], slices.Contains(tl.Lock, p.ID)
		wt := j.wts[p.ID]
		if wt == "" || (len(check) == 0 && !lock) {
			continue
		}
		tf, err := j.projectTestFiles(ctx, *p, wt, check, lock)
		if err != nil {
			logx.Warn(err, "hostrun.log.test_files_failed", "project", p.ID, "job", j.spec.ID)
			continue
		}
		out = append(out, tf)
	}
	return out
}

func (j *job) projectTestFiles(ctx context.Context, p runner.JobProject, wt string, check []string, lock bool) (runner.TestFiles, error) {
	head, err := git(ctx, wt, "rev-parse", "HEAD")
	if err != nil {
		return runner.TestFiles{}, err
	}
	tf := runner.TestFiles{ProjectID: p.ID, Head: head, Blobs: map[string]string{}}
	if len(check) > 0 {
		blobs, berr := treeBlobs(ctx, wt, head, check)
		if berr != nil {
			return runner.TestFiles{}, berr
		}
		tf.Checked, tf.Blobs = check, blobs
	}
	if !lock {
		return tf, nil
	}
	base := defaultCommit(ctx, wt, p.DefaultBranch)
	changed, err := changedFiles(ctx, wt, j.starts[p.ID], head, base)
	if err != nil {
		return runner.TestFiles{}, err
	}
	tf.Changed = changed
	if len(changed) > 0 {
		blobs, err := treeBlobs(ctx, wt, head, changed)
		if err != nil {
			return runner.TestFiles{}, err
		}
		for path, blob := range blobs {
			tf.Blobs[path] = blob
		}
		if base != "" {
			// Without the default branch's blobs the daemon locks every changed test, as it does
			// on its own worktrees.
			if b, err := treeBlobs(ctx, wt, base, changed); err == nil {
				tf.Base = b
			}
		}
	}
	if listed := j.listedTests(); len(listed) > 0 {
		blobs, err := treeBlobs(ctx, wt, head, listed)
		if err != nil {
			return runner.TestFiles{}, err
		}
		tf.Listed = blobs
	}
	return tf, nil
}

// listedTests is the run's locked-tests.txt, cleaned by the daemon's rule; the daemon still keeps
// only the files under the paths it read itself from the uploaded copy.
func (j *job) listedTests() []string {
	body, err := os.ReadFile(filepath.Join(j.root, "runs", j.spec.InstanceID, runner.LockedTestsFile))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		if p := runner.CleanLockedPath(line); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// defaultCommit is the default branch as the clone knows it, without fetching; "" when it does
// not resolve.
func defaultCommit(ctx context.Context, wt, branch string) string {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return ""
	}
	for _, ref := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		if out, err := git(ctx, wt, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return out
		}
	}
	return ""
}

// changedFiles lists the paths that differ between from and to, a rename counted as a delete plus
// an add. An unknown from (the start was not read) means the merge base of to and the default
// branch, as on the daemon.
func changedFiles(ctx context.Context, wt, from, to, base string) ([]string, error) {
	if from == "" {
		if base == "" {
			return nil, apperr.Precondition("hostrun.errors.no_run_start")
		}
		mb, err := git(ctx, wt, "merge-base", base, to)
		if err != nil {
			return nil, err
		}
		from = mb
	}
	if from == to {
		return nil, nil
	}
	out, err := git(ctx, wt, "diff", "--name-only", "--no-renames", "-z", from, to, "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// treeBlobs maps each file at commit under paths to its blob; a path that is not there is left out.
// It reads the tree the way the daemon reads its worktrees, so the two compare.
func treeBlobs(ctx context.Context, wt, commit string, paths []string) (map[string]string, error) {
	out := map[string]string{}
	if len(paths) == 0 {
		return out, nil
	}
	args := append([]string{"--literal-pathspecs", "ls-tree", "-r", "-z", "--full-tree", commit, "--"}, paths...)
	raw, err := git(ctx, wt, args...)
	if err != nil {
		return nil, err
	}
	for _, entry := range strings.Split(raw, "\x00") {
		// "<mode> <type> <sha>\t<path>"
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		out[path] = fields[2]
	}
	return out, nil
}
