// Package gatesprofile is how a project's quality-gate profile reaches thulu-gates on every run:
// the file format and the environment variables, shared by the sandbox and the runner. What a
// profile may hold is checked where it is saved (project.NormalizeGatesProfile); the CLI that
// reads it documents it in images/runtime/thulu-gates/README.md ("Project profile").
package gatesprofile

import "encoding/json"

const (
	// Env names the profile file thulu-gates reads.
	Env = "THULU_GATES_PROFILE"
	// CacheEnv names the directory thulu-gates keeps base results, stored reports and the
	// quarantine in.
	CacheEnv = "THULU_GATES_CACHE"
	// HomeEnv names the copy of the CLI a runner shipped for the job. Another copy found first
	// on PATH hands the run to it (images/runtime/thulu-gates/lib/handover.js).
	HomeEnv = "THULU_GATES_HOME"
)

// File is what a run gets in $THULU_GATES_PROFILE: one profile per project of the run, under the
// directory name thulu-gates finds the repo in. Projects without a profile are left out; nil when
// none has one.
func File(byName map[string]string) []byte {
	entries := map[string]json.RawMessage{}
	for name, profile := range byName {
		if profile != "" && json.Valid([]byte(profile)) {
			entries[name] = json.RawMessage(profile)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	out, err := json.Marshal(map[string]any{"schema": 1, "projects": entries})
	if err != nil {
		return nil
	}
	return out
}
