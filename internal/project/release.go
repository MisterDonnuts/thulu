package project

import (
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// How a project's merged work reaches production: empty (nobody set it), auto (a deploy follows each
// merge) or batch (a release task ships what piled up). release_environment names the GitHub
// environment, or the Evidence environment, that is production.
var releaseMigration = migrate.Migration{ID: 32, SQL: `ALTER TABLE projects
	ADD COLUMN IF NOT EXISTS release_mode        TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS release_environment TEXT NOT NULL DEFAULT ''`}

const (
	ReleaseAuto  = "auto"
	ReleaseBatch = "batch"
)

// GitHub allows more in an environment name, but it reaches a workflow dispatch input and a log
// line: keep it to what those carry unquoted.
var releaseEnvRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,63}$`)

func validateRelease(mode, env string) error {
	switch mode {
	case "", ReleaseAuto, ReleaseBatch:
	default:
		return apperr.Invalid("project.errors.release_mode_invalid",
			"mode", mode, "auto", ReleaseAuto, "batch", ReleaseBatch)
	}
	if env != "" && !releaseEnvRe.MatchString(env) {
		return apperr.Invalid("project.errors.release_environment_invalid", "env", env)
	}
	return nil
}

func (p *Project) applyReleasePatch(mode, env *string) error {
	next, nextEnv := p.ReleaseMode, p.ReleaseEnvironment
	if mode != nil {
		next = strings.TrimSpace(*mode)
	}
	if env != nil {
		nextEnv = strings.TrimSpace(*env)
	}
	if err := validateRelease(next, nextEnv); err != nil {
		return err
	}
	p.ReleaseMode, p.ReleaseEnvironment = next, nextEnv
	return nil
}
