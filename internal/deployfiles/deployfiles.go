package deployfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
)

const (
	RecordFile   = "deploy/.octopus-files.json"
	OverrideFile = "deploy/docker-compose.override.yml"
)

type Record struct {
	Version string            `json:"version,omitempty"`
	Files   map[string]string `json:"files"`
}

func LoadRecord(root string) (*Record, error) {
	raw, err := os.ReadFile(filepath.Join(root, RecordFile)) // #nosec G304 -- fixed name under the install root
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap(err, "deployfiles.errors.read_file", "path", RecordFile)
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, apperr.Wrap(err, "deployfiles.errors.parse_file", "path", RecordFile)
	}
	if r.Files == nil {
		r.Files = map[string]string{}
	}
	return &r, nil
}

func SaveRecord(root string, r *Record) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := fsx.WriteFile(filepath.Join(root, RecordFile), append(body, '\n'), 0o644); err != nil {
		return apperr.Wrap(err, "deployfiles.errors.write_file", "path", RecordFile)
	}
	return nil
}

func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type File struct {
	Body []byte
	Mode fs.FileMode
}

type Tree map[string]File

func (t Tree) Paths() []string {
	out := make([]string, 0, len(t))
	for p := range t {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (t Tree) Hashes() map[string]string {
	out := make(map[string]string, len(t))
	for p, f := range t {
		out[p] = Hash(f.Body)
	}
	return out
}

func FromFS(fsys fs.FS) (Tree, error) {
	t := Tree{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !ValidPath(name) {
			return apperr.Invalid("deployfiles.errors.invalid_path", "path", name)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return apperr.Wrap(err, "deployfiles.errors.read_file", "path", name)
		}
		t[name] = File{Body: body, Mode: ModeFor(name)}
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(err, "deployfiles.errors.tree")
	}
	return t, nil
}

func ModeFor(name string) fs.FileMode {
	if strings.HasSuffix(name, ".sh") {
		return 0o755
	}
	return 0o644
}

var pathChars = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// Operator config and CLI state files that a release bundle must never write.
var reserved = map[string]bool{
	".env":                          true,
	path.Base(OverrideFile):         true,
	path.Base(RecordFile):           true,
	".octopus-update.json":          true,
	".octopus-update.lock":          true,
	".octopus-update.lock.takeover": true,
	"docker-compose.nologin.yml":    true,
}

func ValidPath(p string) bool {
	if p == "" || len(p) > 255 || !pathChars.MatchString(p) || path.Clean(p) != p || path.IsAbs(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return false
		}
	}
	return !reserved[p] && !strings.HasPrefix(p, "values-")
}

type Kind string

const (
	Added     Kind = "added"
	Updated   Kind = "updated"
	Unchanged Kind = "unchanged"
	Edited    Kind = "edited"
	Obsolete  Kind = "obsolete"
)

type Change struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
	Diff string `json:"diff,omitempty"`
}

type Plan struct {
	Version string   `json:"version"`
	Changes []Change `json:"changes"`
}

func (p *Plan) Conflicts() []Change { return p.only(Edited) }

func (p *Plan) Writes() []Change {
	return append(p.only(Added), p.only(Updated)...)
}

func (p *Plan) only(k Kind) []Change {
	var out []Change
	for _, c := range p.Changes {
		if c.Kind == k {
			out = append(out, c)
		}
	}
	return out
}

func NewPlan(root string, rec *Record, baseline map[string]string, target Tree, version string) (*Plan, error) {
	dir, err := os.OpenRoot(filepath.Join(root, "deploy"))
	if err != nil {
		return nil, apperr.Wrap(err, "deployfiles.errors.open_dir")
	}
	defer func() { _ = dir.Close() }()

	known := func(p string) string {
		if rec != nil {
			if h := rec.Files[p]; h != "" {
				return h
			}
		}
		return baseline[p]
	}
	plan := &Plan{Version: version}
	for _, p := range target.Paths() {
		want := target[p]
		disk, err := dir.ReadFile(filepath.FromSlash(p))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			plan.Changes = append(plan.Changes, Change{Path: p, Kind: Added})
			continue
		case err != nil:
			return nil, apperr.Wrap(err, "deployfiles.errors.read_deploy_file", "path", p)
		}
		h := Hash(disk)
		switch {
		case h == Hash(want.Body):
			plan.Changes = append(plan.Changes, Change{Path: p, Kind: Unchanged})
		case h == known(p):
			plan.Changes = append(plan.Changes, Change{Path: p, Kind: Updated, Diff: Diff("deploy/"+p, disk, want.Body)})
		default:
			plan.Changes = append(plan.Changes, Change{Path: p, Kind: Edited, Diff: Diff("deploy/"+p, disk, want.Body)})
		}
	}
	if rec != nil {
		var old []string
		for p := range rec.Files {
			if _, ok := target[p]; !ok {
				old = append(old, p)
			}
		}
		sort.Strings(old)
		for _, p := range old {
			plan.Changes = append(plan.Changes, Change{Path: p, Kind: Obsolete})
		}
	}
	return plan, nil
}

type Undo func() error

func Apply(root string, plan *Plan, target Tree) (*Record, Undo, error) {
	if c := plan.Conflicts(); len(c) > 0 {
		return nil, nil, apperr.Conflict("deployfiles.errors.edited_by_hand", "count", len(c))
	}
	dir, err := os.OpenRoot(filepath.Join(root, "deploy"))
	if err != nil {
		return nil, nil, apperr.Wrap(err, "deployfiles.errors.open_dir")
	}
	defer func() { _ = dir.Close() }()

	j := &journal{root: filepath.Join(root, "deploy")}
	rec := &Record{Version: plan.Version, Files: map[string]string{}}
	for _, c := range plan.Changes {
		f, ok := target[c.Path]
		if !ok {
			continue
		}
		rec.Files[c.Path] = Hash(f.Body)
		if c.Kind != Added && c.Kind != Updated {
			continue
		}
		if err := j.write(dir, c.Path, f); err != nil {
			return nil, nil, errors.Join(err, j.undo())
		}
	}
	return rec, j.undo, nil
}

type journal struct {
	root string
	done []saved
}

type saved struct {
	path string
	body []byte
	mode fs.FileMode
	had  bool
}

func (j *journal) write(dir *os.Root, p string, f File) error {
	local := filepath.FromSlash(p)
	s := saved{path: p}
	if prev, err := dir.ReadFile(local); err == nil {
		info, err := dir.Lstat(local)
		if err != nil {
			return apperr.Wrap(err, "deployfiles.errors.stat_deploy_file", "path", p)
		}
		if !info.Mode().IsRegular() {
			return apperr.Conflict("deployfiles.errors.not_regular", "path", p)
		}
		s.body, s.mode, s.had = prev, info.Mode().Perm(), true
	}
	if err := dir.MkdirAll(filepath.Dir(local), 0o750); err != nil {
		return apperr.Wrap(err, "deployfiles.errors.create_deploy_dir", "path", path.Dir(p))
	}
	// Recorded before the write: a write that fails halfway still has to be undone.
	j.done = append(j.done, s)
	if err := fsx.WriteFile(filepath.Join(j.root, local), f.Body, f.Mode); err != nil {
		return apperr.Wrap(err, "deployfiles.errors.write_deploy_file", "path", p)
	}
	return nil
}

func (j *journal) undo() error {
	var errs []error
	for i := len(j.done) - 1; i >= 0; i-- {
		s := j.done[i]
		full := filepath.Join(j.root, filepath.FromSlash(s.path))
		if !s.had {
			if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := fsx.WriteFile(full, s.body, s.mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
