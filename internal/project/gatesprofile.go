package project

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// The project's quality-gate profile: settings a person keeps in Thulu for thulu-gates, outside
// the client repo (images/runtime/thulu-gates/README.md, "Project profile"). Every run of the
// project gets it as a file named in $THULU_GATES_PROFILE. A branch cannot touch it, so it may
// relax the repo's .thulu/gates.json, and only it can accept a check that cannot be verified. An
// empty column is no profile. How it reaches a run: internal/gatesprofile.
var gatesProfileMigration = migrate.Migration{ID: 34, SQL: `ALTER TABLE projects
	ADD COLUMN IF NOT EXISTS gates_profile TEXT NOT NULL DEFAULT ''`}

// A profile is a few thresholds and reasons; this keeps a pasted log out of every run's files.
const (
	maxGatesProfileBytes = 16 << 10
	maxAcceptReasonRunes = 500
)

// The keys thulu-gates reads: defaults() in images/runtime/thulu-gates/lib/config.js plus
// accept_unverified. TestGatesProfileKeysMatchThuluGates keeps the two lists together.
var gatesProfileKeys = []string{
	"coverage_min", "coverage_warn", "coverage_min_lines", "dup_max", "dup_min_tokens",
	"complexity_max", "mutation_min_efficacy", "new_package_days", "critical", "licenses_allow",
	"semgrep", "perf", "timeouts", "commands", "coverage_files", "accept_unverified",
}

// The check ids of images/runtime/thulu-gates/lib/checks/index.js, the only names
// accept_unverified takes. TestGatesProfileKeysMatchThuluGates keeps them in step.
var gatesCheckIDs = []string{
	"tests", "coverage_new", "duplication", "lint", "typecheck", "sast", "secrets", "deps",
	"licenses", "phantom_deps", "suppressions", "mutation", "perf",
}

var gatesNumberKeys = []string{
	"coverage_min", "coverage_warn", "coverage_min_lines", "dup_max", "dup_min_tokens",
	"complexity_max", "mutation_min_efficacy", "new_package_days",
}

// NormalizeGatesProfile checks a profile and returns it compacted, or "" for an empty one (no
// body, null or {}). It checks shape and names, not every value thulu-gates may reject: an unknown
// key is refused here because a typo would otherwise apply nothing without anyone noticing.
func NormalizeGatesProfile(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if len(trimmed) > maxGatesProfileBytes {
		return "", apperr.Invalid("project.errors.gates_profile_too_big", "max", maxGatesProfileBytes)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &doc); err != nil || doc == nil {
		return "", apperr.Invalid("project.errors.gates_profile_not_object")
	}
	if len(doc) == 0 {
		return "", nil
	}
	for key, val := range doc {
		if err := checkGatesProfileKey(key, val); err != nil {
			return "", err
		}
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return "", apperr.Invalid("project.errors.gates_profile_not_object")
	}
	return buf.String(), nil
}

func checkGatesProfileKey(key string, val json.RawMessage) error {
	switch {
	case !slices.Contains(gatesProfileKeys, key):
		return apperr.Invalid("project.errors.gates_profile_unknown_key",
			"key", key, "keys", strings.Join(gatesProfileKeys, ", "))
	case key == "accept_unverified":
		return checkAcceptUnverified(val)
	case key == "timeouts":
		var m map[string]float64
		if err := json.Unmarshal(val, &m); err != nil {
			return apperr.Invalid("project.errors.gates_profile_bad_value", "key", key)
		}
		for name, v := range m {
			if v < 0 || math.IsNaN(v) {
				return apperr.Invalid("project.errors.gates_profile_bad_value", "key", key+"."+name)
			}
		}
	case key == "commands":
		var m map[string]string
		if err := json.Unmarshal(val, &m); err != nil {
			return apperr.Invalid("project.errors.gates_profile_bad_value", "key", key)
		}
	case slices.Contains(gatesNumberKeys, key):
		var n float64
		if err := json.Unmarshal(val, &n); err != nil || n < 0 {
			return apperr.Invalid("project.errors.gates_profile_bad_value", "key", key)
		}
	}
	return nil
}

func checkAcceptUnverified(val json.RawMessage) error {
	var m map[string]string
	if err := json.Unmarshal(val, &m); err != nil {
		return apperr.Invalid("project.errors.gates_profile_bad_value", "key", "accept_unverified")
	}
	for check, reason := range m {
		if !slices.Contains(gatesCheckIDs, check) {
			return apperr.Invalid("project.errors.gates_profile_unknown_check",
				"check", check, "checks", strings.Join(gatesCheckIDs, ", "))
		}
		why := strings.TrimSpace(reason)
		if why == "" {
			return apperr.Invalid("project.errors.gates_profile_no_reason", "check", check)
		}
		if len([]rune(why)) > maxAcceptReasonRunes {
			return apperr.Invalid("project.errors.gates_profile_reason_too_long", "check", check, "max", maxAcceptReasonRunes)
		}
	}
	return nil
}

func (p *Project) applyGatesProfilePatch(raw *json.RawMessage) error {
	if raw == nil {
		return nil
	}
	norm, err := NormalizeGatesProfile(*raw)
	if err != nil {
		return err
	}
	p.GatesProfile = nil
	if norm != "" {
		p.GatesProfile = json.RawMessage(norm)
	}
	return nil
}
