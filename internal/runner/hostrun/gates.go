package hostrun

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/images"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/gatesprofile"
)

// thulu-gates keeps base results, stored reports and the quarantine in its cache. A job's tmp
// goes with the job, so on a machine the cache lives in the runner's work dir, never in a client
// repo, and every job of every project finds what the last one measured. thulu-gates keeps one
// folder per repo inside it.
func (c *Config) gatesCacheDir() string { return filepath.Join(c.WorkDir, "cache", "thulu-gates") }

// The job's copy of its projects' gate profiles, outside every repo.
func (j *job) gatesProfilePath() string { return filepath.Join(j.root, "thulu-gates", "profile.json") }

// The job's copy of the thulu-gates CLI, laid out as the images do under /opt/thulu-gates, and the
// folder the job puts first on its PATH, with `thulu-gates` in it.
func (j *job) gatesHome() string { return filepath.Join(j.root, "thulu-gates", "cli") }
func (j *job) binDir() string    { return filepath.Join(j.root, "bin") }

// writeGatesCLI writes the thulu-gates this runner binary carries into the job, so the job runs the
// gates of the runner that started it: a machine updates them by updating `thulu`, and a copy a
// person installed by hand (which no update reaches) hands its runs over to this one.
func (j *job) writeGatesCLI() error {
	home := j.gatesHome()
	err := fs.WalkDir(images.ThuluGates, images.ThuluGatesRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := images.ThuluGates.ReadFile(p)
		if err != nil {
			return apperr.Wrap(err, "hostrun.errors.runtime", "file", p)
		}
		full := filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(p, images.ThuluGatesRoot+"/")))
		mode := os.FileMode(0o644)
		if strings.HasPrefix(p, images.ThuluGatesRoot+"/bin/") {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return apperr.Wrap(err, "hostrun.errors.runtime", "file", p)
		}
		if err := os.WriteFile(full, b, mode); err != nil {
			return apperr.Wrap(err, "hostrun.errors.runtime", "file", p)
		}
		return nil
	})
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.runtime_copy", "path", home)
	}
	return j.linkGatesCLI()
}

// linkGatesCLI puts `thulu-gates` in the job's bin folder, pointing at the job's copy; a link left
// by an earlier start of the same job is replaced.
func (j *job) linkGatesCLI() error {
	link := filepath.Join(j.binDir(), "thulu-gates")
	if err := os.MkdirAll(j.binDir(), 0o750); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if err := os.Symlink(filepath.Join(j.gatesHome(), "bin", "thulu-gates.js"), link); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	return nil
}

// writeGatesProfile leaves the profiles of the job's projects where $THULU_GATES_PROFILE points,
// under the names the agent finds the projects by. No profile, no file.
func (j *job) writeGatesProfile() error {
	byName := map[string]string{}
	for i := range j.spec.Projects {
		p := &j.spec.Projects[i]
		byName[mountName(*p)] = string(p.GatesProfile)
	}
	body := gatesprofile.File(byName)
	if body == nil {
		return nil
	}
	path := j.gatesProfilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	return nil
}

// gatesEnv points thulu-gates at the runner's cache (unless the person running the runner chose
// one) and at the job's profile, and puts the job's copy of the CLI first on PATH and in
// $THULU_GATES_HOME. A shell that puts another folder with a thulu-gates ahead again (a .zshrc that
// prepends ~/.local/bin) still runs the job's: that copy sees $THULU_GATES_HOME and hands over. A
// profile from the person's shell is dropped: the project's lives in Thulu.
func (j *job) gatesEnv(m map[string]string) {
	m[gatesprofile.HomeEnv] = j.gatesHome()
	if p := m["PATH"]; p != "" {
		m["PATH"] = j.binDir() + string(os.PathListSeparator) + p
	} else {
		m["PATH"] = j.binDir() + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	if m[gatesprofile.CacheEnv] == "" {
		m[gatesprofile.CacheEnv] = j.r.cfg.gatesCacheDir()
	}
	delete(m, gatesprofile.Env)
	if _, err := os.Stat(j.gatesProfilePath()); err == nil {
		m[gatesprofile.Env] = j.gatesProfilePath()
	}
}
