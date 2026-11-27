package runner

import (
	"path"
	"strings"
)

// Locked tests on a machine's code. The daemon locks the tests a lock_tests stage writes and checks
// that later stages leave them alone by their git blobs; for a project whose code lives on a runner
// it has no tree to read, so the job asks the runner to read it at the run's end (JobSpec.TestLock)
// and the runner sends the blobs back with the run's end (FinishRequest.TestFiles). A runner that
// predates this sends nothing, and the daemon says the lock could not be checked, as before.

// LockedTestsFile is where a lock_tests run lists, one per line, the paths it locks beyond the files
// it changed (a directory locks every file under it).
const LockedTestsFile = "locked-tests.txt"

// TestLock is what the runner reports about the job's tests.
type TestLock struct {
	// The job is a lock_tests stage run: for each of these projects, report the files the run
	// changed and the ones its locked-tests.txt lists.
	Lock []string `json:"lock,omitempty"`
	// The locked paths to read at the run's end, by project id.
	Check map[string][]string `json:"check,omitempty"`
}

// TestFiles is one project's tree at the end of a run, as git blob ids by repository-relative path.
// A path the runner was asked about and left out of a map was absent from the tree.
type TestFiles struct {
	ProjectID string `json:"project_id"`
	// The commit the blobs were read from.
	Head string `json:"head"`
	// The TestLock.Check paths the runner read; only these may be compared.
	Checked []string `json:"checked,omitempty"`
	// Blobs at Head of the checked paths and, in a lock run, of the files the run changed.
	Blobs map[string]string `json:"blobs,omitempty"`
	// Lock run: the files the run added, changed or deleted.
	Changed []string `json:"changed,omitempty"`
	// Lock run: the changed files' blobs on the default branch; nil when it does not resolve.
	Base map[string]string `json:"base,omitempty"`
	// Lock run: the files under the paths locked-tests.txt lists, with their blobs at Head.
	Listed map[string]string `json:"listed,omitempty"`
}

// CleanLockedPath is one repository-relative path from locked-tests.txt, cleaned. "" for a blank
// line, a comment, or a path that is absolute or leaves the repository. The daemon and the runner
// read the file by this same rule.
func CleanLockedPath(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	p := path.Clean(strings.ReplaceAll(line, `\`, "/"))
	if path.IsAbs(p) || p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsRune(p, 0) {
		return ""
	}
	return p
}
