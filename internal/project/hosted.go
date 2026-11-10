package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const (
	hostedDirName = ".hosted"
	// Kept inside the hosted directory so the finished repository can be moved there with os.Rename
	// (same filesystem).
	incomingDirName = ".incoming"
	// Only the daemon can write a clone's .git (agents see it read-only), so this marker cannot be
	// forged; it unlocks the file transport.
	hostedMarker = "octopus-hosted"
)

// The one identity Thulu commits as, in hosted repos and in the clones it keeps.
const (
	commitName  = "Thulu" //i18n:ignore git author name, an identity rather than a message
	commitEmail = "noreply@thulu.dev"
)

var commitIdentity = []string{"-c", "user.name=" + commitName, "-c", "user.email=" + commitEmail}

// Hosted paths are built from the id, and an imported bundle can carry any string as one.
var hostedIDPattern = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

func (s *Store) hostedRoot() string { return filepath.Join(s.baseDir, hostedDirName) }

func (s *Store) hostedRepoDir(id string) string {
	if !hostedIDPattern.MatchString(id) {
		return filepath.Join(s.hostedRoot(), "invalid.git")
	}
	return filepath.Join(s.hostedRoot(), id+".git")
}

func hostedClone(projPath string) bool {
	fi, err := os.Lstat(filepath.Join(projPath, ".git", hostedMarker))
	return err == nil && fi.Mode().IsRegular()
}

func bareGit(dir string) localGitExec { return localGitExec{gitDir: dir, hosted: true} }

// update-ref's old-value check would catch a race; the lock keeps it from surfacing as an error.
func (s *Store) hostedLock(id string) *sync.Mutex {
	return s.hostedLocks.Get(id)
}

func (s *Store) IncomingFile() (*os.File, error) {
	dir := filepath.Join(s.hostedRoot(), incomingDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, apperr.Wrap(err, "project.errors.upload_unpack")
	}
	f, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.upload_unpack")
	}
	return f, nil
}

// The CLI opens a store too while the daemon may be mid-upload, so only entries older than any live
// request are removed.
func (s *Store) clearIncoming() {
	dir := filepath.Join(s.hostedRoot(), incomingDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-6 * time.Hour)
	for _, e := range entries {
		if info, infoErr := e.Info(); infoErr == nil && info.ModTime().Before(cutoff) {
			p := filepath.Join(dir, e.Name())
			logx.Warn(os.RemoveAll(p), "project.log.clear_incoming", "path", p)
		}
	}
}

func (s *Store) workDir() (string, error) {
	dir := filepath.Join(s.hostedRoot(), incomingDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", apperr.Wrap(err, "project.errors.upload_unpack")
	}
	w, err := os.MkdirTemp(dir, "work-*")
	if err != nil {
		return "", apperr.Wrap(err, "project.errors.upload_unpack")
	}
	return w, nil
}

func (s *Store) claimProjectDir(name, id string) (string, error) {
	dirName := sanitizeDirName(name)
	if err := checkProjectDirName(name, dirName); err != nil {
		return "", err
	}
	projPath := filepath.Join(s.baseDir, dirName)
	if _, err := os.Stat(projPath); err == nil {
		dirName = dirName + "-" + id
		projPath = filepath.Join(s.baseDir, dirName)
		if _, err := os.Stat(projPath); err == nil {
			return "", apperr.Conflict("project.errors.dir_exists", "dir", dirName)
		}
	}
	if err := os.MkdirAll(s.baseDir, 0o750); err != nil {
		return "", apperr.Wrap(err, "project.errors.base_dir_failed")
	}
	return projPath, nil
}

func (s *Store) CreateFromUpload(ctx context.Context, name, src string, opts CreateOpts, lim UploadLimits) (*Project, UploadReport, error) {
	if strings.TrimSpace(name) == "" {
		return nil, UploadReport{}, apperr.Invalid("project.errors.name_required")
	}
	if strings.TrimSpace(opts.WorkspaceID) == "" {
		return nil, UploadReport{}, apperr.Invalid("project.errors.workspace_required")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx,
		s.rb(`SELECT COUNT(*) FROM projects WHERE workspace_id = ? AND name = ?`),
		opts.WorkspaceID, name,
	).Scan(&exists); err == nil && exists > 0 {
		return nil, UploadReport{}, apperr.Conflict("project.errors.name_taken", "name", name)
	}

	id := generateID()
	projPath, err := s.claimProjectDir(name, id)
	if err != nil {
		return nil, UploadReport{}, err
	}
	work, err := s.workDir()
	if err != nil {
		return nil, UploadReport{}, err
	}
	defer func() { logx.Warn(os.RemoveAll(work), "project.log.remove_upload_work", "path", work) }()
	up, err := unpackUpload(src, work, lim)
	if err != nil {
		return nil, UploadReport{}, err
	}
	// Drop the marker: it names a commit of another repository and must not be committed.
	takeBaseMarker(up)

	bare := s.hostedRepoDir(id)
	fail := func(err error) (*Project, UploadReport, error) {
		logx.Warn(os.RemoveAll(bare), "project.log.remove_hosted_repo", "path", bare)
		logx.Warn(os.RemoveAll(projPath), "project.log.cleanup_clone_dir", "path", projPath)
		return nil, UploadReport{}, err
	}
	report := up.report
	branch, commit, err := seedHosted(ctx, bare, up, &report)
	if err != nil {
		return fail(err)
	}
	if err = cloneHosted(bare, projPath); err != nil {
		return fail(err)
	}
	report.Branch, report.Commit, report.Changed = branch, commit, true

	tpl := opts.StageTemplate
	if tpl == nil {
		tpl = map[string]string{}
	}
	proj := &Project{
		ID:             id,
		Name:           name,
		Path:           projPath,
		DefaultBranch:  branch,
		Description:    opts.Description,
		StageTemplate:  tpl,
		DefaultBoardID: opts.DefaultBoardID,
		WorkspaceID:    opts.WorkspaceID,
		Source:         SourceUpload,
		CreatedAt:      time.Now().UTC(),
	}
	tplJSON, err := marshalTemplate(tpl)
	if err != nil {
		return fail(err)
	}
	if err = s.insertProject(ctx, proj, tplJSON); err != nil {
		return fail(err)
	}
	return proj, report, nil
}

func seedHosted(ctx context.Context, bare string, up *unpacked, report *UploadReport) (branch, commit string, err error) {
	base := ""
	if up.hasGit {
		branch, base, err = adoptUploadedGit(ctx, up.git, bare)
		switch {
		case errors.Is(err, errNoHistory):
			logx.Warn(os.RemoveAll(bare), "project.log.remove_hosted_repo", "path", bare)
			report.HistoryIgnored = true
		case err != nil:
			return "", "", err
		default:
			report.History = true
		}
	}
	if branch == "" {
		branch = "main"
		if ierr := initBare(bare, branch); ierr != nil {
			return "", "", ierr
		}
	}
	if report.Files == 0 {
		if base == "" {
			return "", "", apperr.Invalid("project.errors.upload_empty")
		}
		// History but no files: committing the empty folder would delete every file.
		return branch, base, nil
	}
	msg := i18n.TCtx(ctx, "project.commits.upload_initial")
	if base != "" {
		msg = i18n.TCtx(ctx, "project.commits.upload_version")
	}
	commit, changed, err := commitUpload(bare, up, base, msg)
	if err != nil {
		return "", "", err
	}
	if changed {
		if err = bareGit(bare).Run(bare, "update-ref", "refs/heads/"+branch, commit, refOld(base)); err != nil {
			return "", "", apperr.Wrap(err, "project.errors.upload_commit")
		}
	}
	return branch, commit, nil
}

// Written into branch zips by ArchiveBranch and read back by ReuploadProject to detect that the
// default branch moved. Never committed.
const baseMarker = ".octopus-base"

// An object name plus newline; a bigger file with this name belongs to the project.
const maxBaseMarkerBytes = 128

func takeBaseMarker(up *unpacked) string {
	p := filepath.Join(up.files, baseMarker)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxBaseMarkerBytes {
		return ""
	}
	raw, err := os.ReadFile(p) //nolint:gosec // G304: inside the daemon's unpack dir
	if err != nil {
		return ""
	}
	if err := os.Remove(p); err != nil {
		logx.Warn(err, "project.log.remove_base_marker", "path", p)
		return ""
	}
	up.report.Files--
	up.report.Bytes -= fi.Size()
	sha := strings.TrimSpace(string(raw))
	if !isHexObject(sha) {
		return ""
	}
	return sha
}

const (
	ReuploadReplace = "replace"
	ReuploadMerge   = "merge"
)

type ReuploadOpts struct {
	Mode string
}

type MainMovedError struct {
	Branch string
	Base   string
	Ahead  int
}

func (e *MainMovedError) Error() string {
	return i18n.Log("project.errors.main_moved", "branch", e.Branch, "count", e.Ahead)
}

type ReuploadConflictError struct {
	Branch string
	Files  []string
}

func (e *ReuploadConflictError) Error() string {
	return i18n.Log("project.errors.reupload_conflict", "branch", e.Branch, "count", len(e.Files))
}

func (s *Store) ReuploadProject(ctx context.Context, id, src string, lim UploadLimits, opts ReuploadOpts) (UploadReport, error) {
	proj, err := s.Get(ctx, id)
	if err != nil {
		return UploadReport{}, err
	}
	if !proj.Hosted() {
		return UploadReport{}, apperr.Invalid("project.errors.not_upload_project", "name", proj.Name)
	}
	if verr := ValidateBranchName(proj.DefaultBranch); verr != nil {
		return UploadReport{}, verr
	}
	lock := s.hostedLock(id)
	lock.Lock()
	defer lock.Unlock()

	work, err := s.workDir()
	if err != nil {
		return UploadReport{}, err
	}
	defer func() { logx.Warn(os.RemoveAll(work), "project.log.remove_upload_work", "path", work) }()
	up, err := unpackUpload(src, work, lim)
	if err != nil {
		return UploadReport{}, err
	}
	from := takeBaseMarker(up)
	report := up.report
	report.HistoryIgnored = up.hasGit
	if report.Files == 0 {
		return UploadReport{}, apperr.Invalid("project.errors.upload_empty")
	}
	bare := s.hostedRepoDir(id)
	ref := "refs/heads/" + proj.DefaultBranch
	head, err := revParse(bare, ref)
	if err != nil {
		return UploadReport{}, err
	}
	msg := i18n.TCtx(ctx, "project.commits.upload_version")
	var (
		commit, version string
		changed         bool
	)
	ahead := commitsSince(bare, from, head)
	switch {
	case ahead > 0 && opts.Mode == ReuploadMerge:
		commit, version, changed, err = mergeUpload(ctx, bare, up, from, head, proj.DefaultBranch, msg)
	case ahead > 0 && opts.Mode != ReuploadReplace:
		return UploadReport{}, &MainMovedError{Branch: proj.DefaultBranch, Base: from, Ahead: ahead}
	default:
		commit, changed, err = commitUpload(bare, up, head, msg)
	}
	if err != nil {
		return UploadReport{}, err
	}
	if changed {
		if err := bareGit(bare).Run(bare, "update-ref", ref, commit, head); err != nil {
			return UploadReport{}, apperr.Wrap(err, "project.errors.upload_commit")
		}
		logx.Warn(fetchHostedBranch(proj, proj.DefaultBranch), "project.log.fetch_origin", "branch", proj.DefaultBranch, "path", proj.Path)
	}
	report.Branch, report.Commit, report.Changed = proj.DefaultBranch, commit, changed
	if changed {
		report.Version = version
	}
	return report, nil
}

// from need not be on head's line (a folder downloaded from a task branch): count from the
// merge-base, or replacing head undoes what landed since.
// 0 when from is unknown or shares no history with head.
func commitsSince(bare, from, head string) int {
	if from == "" || from == head {
		return 0
	}
	g := bareGit(bare)
	if _, err := revParse(bare, from+"^{commit}"); err != nil {
		return 0
	}
	base, err := g.Output(bare, "merge-base", from, head)
	if err != nil {
		return 0
	}
	out, err := g.Output(bare, "rev-list", "--count", strings.TrimSpace(base)+".."+head)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return n
}

func mergeUpload(ctx context.Context, bare string, up *unpacked, from, head, branch, msg string) (merge, version string, changed bool, err error) {
	version, changed, err = commitUpload(bare, up, from, msg)
	if err != nil {
		return "", "", false, err
	}
	g := bareGit(bare)
	if !changed {
		// Unchanged folder: from the branch itself it brings nothing; from another branch it brings
		// that branch's commits, so from is merged.
		if g.Run(bare, "merge-base", "--is-ancestor", from, head) == nil {
			return head, "", false, nil
		}
	}
	tree, conflicts, err := mergeTree(ctx, g, bare, head, version)
	if err != nil {
		return "", "", false, err
	}
	if len(conflicts) > 0 {
		return "", "", false, &ReuploadConflictError{Branch: branch, Files: conflicts}
	}
	headTree, err := revParse(bare, head+"^{tree}")
	if err != nil {
		return "", "", false, err
	}
	if headTree == tree {
		return head, "", false, nil
	}
	subject := i18n.TCtx(ctx, "project.commits.upload_merge", "branch", branch)
	args := append(append([]string{}, commitIdentity...), "commit-tree", tree, "-p", head, "-p", version, "-m", subject)
	out, err := g.Output(bare, args...)
	if err != nil {
		return "", "", false, apperr.Wrap(err, "project.errors.upload_commit")
	}
	return strings.TrimSpace(out), version, true, nil
}

func refOld(base string) string {
	if base == "" {
		return strings.Repeat("0", 40)
	}
	return base
}

func marshalTemplate(tpl map[string]string) (string, error) {
	raw, err := json.Marshal(tpl)
	if err != nil {
		return "", apperr.Wrap(err, "project.errors.marshal_template")
	}
	return string(raw), nil
}

func initBare(dir, branch string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return apperr.Wrap(err, "project.errors.upload_commit")
	}
	if err := (localGitExec{}).Run("", "init", "--bare", "--initial-branch="+branch, "--", dir); err != nil {
		return apperr.Wrap(err, "project.errors.upload_commit")
	}
	return nil
}

var errNoHistory = apperr.Invalid("project.errors.upload_no_history")

// The upload's config, hooks and info are never copied: the daemon writes its own config (an
// uploaded core.sshCommand would run on the host).
func adoptUploadedGit(ctx context.Context, src, bare string) (branch, tip string, err error) {
	branch, detached, err := readUploadedHead(src)
	if err != nil {
		return "", "", err
	}
	if merr := moveUploadedGit(src, bare, branch); merr != nil {
		return "", "", merr
	}
	g := bareGit(bare)
	if detached != "" {
		if err = g.Run(bare, "update-ref", "refs/heads/"+branch, detached); err != nil {
			return "", "", errNoHistory
		}
	}
	refs, err := g.Output(bare, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return "", "", apperr.Wrap(err, "project.errors.git_for_each_ref")
	}
	for _, ref := range strings.Fields(refs) {
		if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			logx.Warn(g.Run(bare, "update-ref", "-d", ref), "project.log.drop_uploaded_ref", "ref", ref)
		}
	}
	tip, err = revParse(bare, "refs/heads/"+branch)
	if err != nil {
		return "", "", errNoHistory
	}
	// A partial or broken upload would otherwise fail at the first task.
	fsckCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd, err := g.commandContext(fsckCtx, bare, []string{"fsck", "--connectivity-only", "--no-dangling", "--no-progress"})
	if err != nil {
		return "", "", err
	}
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return "", "", apperr.Wrap(runErr, "project.errors.upload_git_broken", "output", strings.TrimSpace(string(out)))
	}
	return branch, tip, nil
}

func readUploadedHead(src string) (branch, detached string, err error) {
	head, err := os.ReadFile(filepath.Join(src, "HEAD")) //nolint:gosec // G304: inside the daemon's unpack dir
	if err != nil {
		return "", "", errNoHistory
	}
	for _, d := range []string{"objects", "refs"} {
		if fi, statErr := os.Stat(filepath.Join(src, d)); statErr != nil || !fi.IsDir() {
			return "", "", errNoHistory
		}
	}
	// moveUploadedGit writes a config that cannot describe these formats; git would misread the
	// repository.
	if cfg, readErr := os.ReadFile(filepath.Join(src, "config")); readErr == nil { //nolint:gosec // G304: inside the daemon's unpack dir
		low := strings.ToLower(string(cfg))
		if strings.Contains(low, "objectformat") || strings.Contains(low, "refstorage") {
			return "", "", apperr.Invalid("project.errors.upload_git_format")
		}
	}
	h := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(h, "ref: refs/heads/"); ok && ValidateBranchName(ref) == nil { //i18n:ignore git HEAD syntax
		return ref, "", nil
	}
	if isHexObject(h) {
		return "main", h, nil
	}
	return "", "", errNoHistory
}

func moveUploadedGit(src, bare, branch string) error {
	wrap := func(err error) error { return apperr.Wrap(err, "project.errors.upload_commit") }
	if err := os.MkdirAll(bare, 0o750); err != nil {
		return wrap(err)
	}
	for _, d := range []string{"objects", "refs"} {
		if err := os.Rename(filepath.Join(src, d), filepath.Join(bare, d)); err != nil {
			return wrap(err)
		}
	}
	for _, f := range []string{"packed-refs", "shallow"} {
		fi, statErr := os.Lstat(filepath.Join(src, f))
		if statErr != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := os.Rename(filepath.Join(src, f), filepath.Join(bare, f)); err != nil {
			return wrap(err)
		}
	}
	for _, d := range []string{"objects/info", "objects/pack", "refs/heads", "refs/tags"} {
		if err := os.MkdirAll(filepath.Join(bare, d), 0o750); err != nil {
			return wrap(err)
		}
	}
	config := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n"
	if err := os.WriteFile(filepath.Join(bare, "config"), []byte(config), 0o600); err != nil {
		return wrap(err)
	}
	head := []byte("ref: refs/heads/" + branch + "\n") //i18n:ignore git HEAD syntax
	if err := os.WriteFile(filepath.Join(bare, "HEAD"), head, 0o600); err != nil {
		return wrap(err)
	}
	return nil
}

// up.keep excludes the paths the upload skipped on purpose, so a file base tracked and the upload
// skipped stays as it was.
func commitUpload(bare string, up *unpacked, base, message string) (commit string, changed bool, err error) {
	index := filepath.Join(filepath.Dir(up.files), "index")
	g := localGitExec{gitDir: bare, workTree: up.files, index: index, hosted: true}
	// Existing files keep base's mode: the browser cannot tell an executable from any other file.
	fileMode := "core.fileMode=true"
	if base != "" {
		fileMode = "core.fileMode=false"
		if err = g.Run(up.files, "read-tree", base); err != nil {
			return "", false, apperr.Wrap(err, "project.errors.upload_commit")
		}
	}
	specs := filepath.Join(filepath.Dir(up.files), "pathspecs")
	lines := append([]string{"."}, up.keep...)
	if err = os.WriteFile(specs, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return "", false, apperr.Wrap(err, "project.errors.upload_commit")
	}
	if err = g.Run(up.files, "-c", fileMode, "-c", "core.autocrlf=false", "add", "-A", "--pathspec-from-file="+specs); err != nil {
		return "", false, apperr.Wrap(err, "project.errors.upload_commit")
	}
	tree, err := g.Output(up.files, "write-tree")
	if err != nil {
		return "", false, apperr.Wrap(err, "project.errors.upload_commit")
	}
	tree = strings.TrimSpace(tree)
	args := append(append([]string{}, commitIdentity...), "commit-tree", tree, "-m", message)
	if base != "" {
		baseTree, parseErr := revParse(bare, base+"^{tree}")
		if parseErr != nil {
			return "", false, parseErr
		}
		if baseTree == tree {
			return base, false, nil
		}
		args = append(args, "-p", base)
	}
	out, err := g.Output(up.files, args...)
	if err != nil {
		return "", false, apperr.Wrap(err, "project.errors.upload_commit")
	}
	return strings.TrimSpace(out), true, nil
}

func revParse(dir, ref string) (string, error) {
	out, err := bareGit(dir).Output(dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if err != nil {
		return "", apperr.Wrap(err, "project.errors.hosted_ref_missing", "ref", ref)
	}
	return strings.TrimSpace(out), nil
}

func cloneHosted(bare, dest string) error {
	if err := (localGitExec{hosted: true}).Run("", "clone", "--no-recurse-submodules", "--", bare, dest); err != nil {
		return apperr.Wrap(err, "project.errors.upload_clone")
	}
	if err := os.WriteFile(filepath.Join(dest, ".git", hostedMarker), []byte("1\n"), 0o600); err != nil {
		return apperr.Wrap(err, "project.errors.upload_clone")
	}
	logx.Warn(gitRun(dest, "config", "user.email", commitEmail),
		"project.log.git_config", "setting", "user.email", "path", dest)
	logx.Warn(gitRun(dest, "config", "user.name", commitName),
		"project.log.git_config", "setting", "user.name", "path", dest)
	return nil
}

func fetchHostedBranch(proj *Project, branch string) error {
	return gitRun(proj.Path, "fetch", "--no-recurse-submodules", "--", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch)
}

const (
	MergeFastForward = "fast_forward"
	MergeMerged      = "merged"
	MergeUpToDate    = "up_to_date"
	MergeConflict    = "conflict"
)

type MergeResult struct {
	Status    string   `json:"status"`
	Branch    string   `json:"branch"`
	Target    string   `json:"target"`
	Commit    string   `json:"commit,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// The caller must push the task branch first.
func (s *Store) MergeInto(ctx context.Context, projectID, branch, target string) (MergeResult, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return MergeResult{}, err
	}
	if !proj.Hosted() {
		return MergeResult{}, apperr.Invalid("project.errors.not_upload_project", "name", proj.Name)
	}
	for _, b := range []string{branch, target} {
		if verr := ValidateBranchName(b); verr != nil {
			return MergeResult{}, verr
		}
	}
	lock := s.hostedLock(projectID)
	lock.Lock()
	defer lock.Unlock()

	bare := s.hostedRepoDir(projectID)
	g := bareGit(bare)
	res := MergeResult{Branch: branch, Target: target}
	tip, err := revParse(bare, "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return res, apperr.NotFound("project.errors.merge_branch_missing", "branch", branch)
	}
	head, err := revParse(bare, "refs/heads/"+target+"^{commit}")
	if err != nil {
		return res, apperr.Invalid("project.errors.merge_target_missing", "branch", target)
	}
	switch {
	case g.Run(bare, "merge-base", "--is-ancestor", tip, head) == nil:
		res.Status, res.Commit = MergeUpToDate, head
		return res, nil
	case g.Run(bare, "merge-base", "--is-ancestor", head, tip) == nil:
		if err := g.Run(bare, "update-ref", "refs/heads/"+target, tip, head); err != nil {
			return res, apperr.Wrap(err, "project.errors.merge_failed")
		}
		res.Status, res.Commit = MergeFastForward, tip
	default:
		tree, conflicts, err := mergeTree(ctx, g, bare, head, tip)
		if err != nil {
			return res, err
		}
		if len(conflicts) > 0 {
			res.Status, res.Conflicts = MergeConflict, conflicts
			return res, nil
		}
		msg := i18n.TCtx(ctx, "project.commits.merge", "branch", branch, "target", target)
		args := append(append([]string{}, commitIdentity...), "commit-tree", tree, "-p", head, "-p", tip, "-m", msg)
		out, err := g.Output(bare, args...)
		if err != nil {
			return res, apperr.Wrap(err, "project.errors.merge_failed")
		}
		commit := strings.TrimSpace(out)
		if err := g.Run(bare, "update-ref", "refs/heads/"+target, commit, head); err != nil {
			return res, apperr.Wrap(err, "project.errors.merge_failed")
		}
		res.Status, res.Commit = MergeMerged, commit
	}
	logx.Warn(fetchHostedBranch(proj, target), "project.log.fetch_origin", "branch", target, "path", proj.Path)
	return res, nil
}

// git merge-tree --write-tree exits 1 on conflicts; that is an answer here, not a failure.
func mergeTree(ctx context.Context, g localGitExec, bare, ours, theirs string) (string, []string, error) {
	cmd, err := g.commandContext(ctx, bare, []string{"merge-tree", "--write-tree", "--name-only", "--no-messages", ours, theirs})
	if err != nil {
		return "", nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	var exit *exec.ExitError
	switch {
	case runErr == nil:
		return strings.TrimSpace(lines[0]), nil, nil
	case errors.As(runErr, &exit) && exit.ExitCode() == 1 && len(lines) > 1:
		var files []string
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				files = append(files, l)
			}
		}
		return "", files, nil
	}
	return "", nil, apperr.Wrap(runErr, "project.errors.merge_tree", "output", strings.TrimSpace(stderr.String()))
}

func (s *Store) Branches(ctx context.Context, projectID string) ([]string, error) {
	all, err := s.branchNames(ctx, projectID)
	if err != nil {
		return nil, err
	}
	branches := []string{}
	for _, name := range all {
		if strings.HasPrefix(name, taskBranchName("")) || strings.HasPrefix(name, ResultBranchName("")) {
			continue
		}
		branches = append(branches, name)
	}
	return branches, nil
}

func (s *Store) TaskBranches(ctx context.Context, projectID string) ([]string, error) {
	all, err := s.branchNames(ctx, projectID)
	if err != nil {
		return nil, err
	}
	tasks := []string{}
	for _, name := range all {
		if id, ok := strings.CutPrefix(name, taskBranchName("")); ok && validateTaskID(id) == nil {
			tasks = append(tasks, id)
		}
	}
	return tasks, nil
}

func (s *Store) branchNames(ctx context.Context, projectID string) ([]string, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	dir, prefix, g := proj.Path, "refs/remotes/origin/", repoGit(proj.Path)
	if proj.Hosted() {
		dir, prefix = s.hostedRepoDir(projectID), "refs/heads/"
		g = bareGit(dir)
	}
	out, err := g.Output(dir, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.git_for_each_ref")
	}
	names := []string{}
	for _, ref := range strings.Fields(out) {
		if name := strings.TrimPrefix(ref, prefix); name != "HEAD" {
			names = append(names, name)
		}
	}
	return names, nil
}

func (s *Store) ArchiveBranch(ctx context.Context, projectID, branch string) (io.ReadCloser, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if verr := ValidateBranchName(branch); verr != nil {
		return nil, verr
	}
	if proj.Hosted() {
		if _, err = revParse(s.hostedRepoDir(projectID), "refs/heads/"+branch); err != nil {
			return nil, apperr.NotFound("project.errors.archive_branch_missing", "branch", branch)
		}
		logx.Warn(fetchHostedBranch(proj, branch), "project.log.fetch_origin", "branch", branch, "path", proj.Path)
	}
	commit, err := resolveCommit(proj.Path, "refs/remotes/origin/"+branch)
	if err != nil {
		return nil, apperr.NotFound("project.errors.archive_branch_missing", "branch", branch)
	}
	if !proj.Hosted() {
		return archiveCommit(ctx, proj.Path, commit, "zip")
	}
	return archiveCommit(ctx, proj.Path, commit, "zip", "--add-virtual-file="+baseMarker+":"+commit+"\n")
}

func (s *Store) HostedBytes(ctx context.Context, workspaceID string) (int64, error) {
	projects, err := s.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	var total int64
	for i := range projects {
		if projects[i].Hosted() {
			total += dirBytes(s.hostedRepoDir(projects[i].ID))
		}
	}
	return total, nil
}

func dirBytes(dir string) int64 {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a file gone mid-walk just isn't counted
		}
		if info, infoErr := d.Info(); infoErr == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	logx.Warn(err, "project.log.measure_hosted", "path", dir)
	return n
}

func (s *Store) ExportCode(ctx context.Context, p *Project) ([]byte, error) {
	if !p.Hosted() {
		return nil, nil
	}
	work, err := s.workDir()
	if err != nil {
		return nil, err
	}
	defer func() { logx.Warn(os.RemoveAll(work), "project.log.remove_upload_work", "path", work) }()
	out := filepath.Join(work, "code.bundle")
	bare := s.hostedRepoDir(p.ID)
	cmd, err := bareGit(bare).commandContext(ctx, bare, []string{"bundle", "create", out, "--all"})
	if err != nil {
		return nil, err
	}
	if msg, runErr := cmd.CombinedOutput(); runErr != nil {
		return nil, apperr.Wrap(runErr, "project.errors.export_code", "id", p.ID, "output", strings.TrimSpace(string(msg)))
	}
	return os.ReadFile(out) //nolint:gosec // G304: the daemon's own temp file
}

func (s *Store) RestoreCode(_ context.Context, p *Project, bundle []byte) error {
	if !p.Hosted() || len(bundle) == 0 {
		return nil
	}
	if filepath.Dir(p.Path) != filepath.Clean(s.baseDir) {
		return apperr.Invalid("project.errors.restore_path", "id", p.ID, "path", p.Path)
	}
	bare := s.hostedRepoDir(p.ID)
	if _, err := os.Stat(bare); errors.Is(err, fs.ErrNotExist) {
		work, err := s.workDir()
		if err != nil {
			return err
		}
		defer func() { logx.Warn(os.RemoveAll(work), "project.log.remove_upload_work", "path", work) }()
		file := filepath.Join(work, "code.bundle")
		if err := os.WriteFile(file, bundle, 0o600); err != nil {
			return apperr.Wrap(err, "project.errors.restore_code", "id", p.ID)
		}
		if err := os.MkdirAll(filepath.Dir(bare), 0o750); err != nil {
			return apperr.Wrap(err, "project.errors.restore_code", "id", p.ID)
		}
		if err := (localGitExec{hosted: true}).Run("", "clone", "--bare", "--no-recurse-submodules", "--", file, bare); err != nil {
			return apperr.Wrap(err, "project.errors.restore_code", "id", p.ID)
		}
		logx.Warn(bareGit(bare).Run(bare, "remote", "remove", "origin"), "project.log.restore_remote", "id", p.ID)
	}
	if _, err := os.Stat(p.Path); errors.Is(err, fs.ErrNotExist) {
		return cloneHosted(bare, p.Path)
	}
	return nil
}
