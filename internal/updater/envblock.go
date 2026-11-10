package updater

import (
	"os"
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/fsx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kept"
)

// apply rewrites the managed block of deploy/.env whole; the rest is the operator's.
// Compose reads image refs from it and env_file passes it to the daemon, whose janitor keeps the
// images listed.
const (
	// Match these markers by prefix: the header text after blockStart is written in the daemon's
	// locale.
	blockStart = "# >>> octopus apply" //i18n:ignore file marker parsed back by readBlock
	blockEnd   = "# <<< octopus apply" //i18n:ignore file marker parsed back by readBlock
)

// OCTOPUS_AGENT_IMAGE must stay unmanaged: it is the operator's image for every engine. Older
// releases pinned it inside the block; rewriting the block drops that pin.
const (
	envDaemonImage   = "OCTOPUS_IMAGE"
	envSandboxImage  = "OCTOPUS_SANDBOX_IMAGE"
	envPGImage       = "OCTOPUS_PG_IMAGE"
	envCaddyImage    = "OCTOPUS_CADDY_IMAGE"
	envKeptImages    = dockergc.EnvKeptImages
	envKeptVersions  = kept.EnvVersions
	envKeptSnapshots = kept.EnvSnapshots
)

func managedKeys() []string {
	keys := []string{envDaemonImage}
	for _, name := range dockergc.NativeMCPServers {
		keys = append(keys, dockergc.MCPImageEnv(name))
	}
	for _, e := range agentimage.Engines {
		keys = append(keys, agentimage.Env(e))
	}
	return append(keys, envSandboxImage, envPGImage, envCaddyImage,
		envKeptVersions, envKeptSnapshots, envKeptImages)
}

// The old updater's rollback window; left live, the janitor would keep honoring a stale image.
var supersededKeys = []string{dockergc.EnvRollbackImage, dockergc.EnvRollbackUntil}

var assignment = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// Only values that need no quoting are allowed, so nothing can inject into .env.
var envValue = regexp.MustCompile(`^[A-Za-z0-9._:/@,=+-]*$`)

func readBlock(raw string) map[string]string {
	out := map[string]string{}
	in := false
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, blockStart):
			in = true
		case strings.HasPrefix(line, blockEnd):
			in = false
		case in:
			if k, v, ok := strings.Cut(line, "="); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

func writeBlock(raw string, values map[string]string) (string, []string, error) {
	allowed := map[string]bool{}
	for _, k := range managedKeys() {
		allowed[k] = true
	}
	for k, v := range values {
		if !allowed[k] {
			return "", nil, apperr.Invalid("updater.errors.not_managed_key", "key", k)
		}
		if !envValue.MatchString(v) {
			return "", nil, apperr.Invalid("updater.errors.refusing_env_value", "key", k, "value", v)
		}
	}
	disable := map[string]bool{}
	for _, k := range append(managedKeys(), supersededKeys...) {
		disable[k] = true
	}

	lines, disabled := outsideBlock(raw, disable)
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(blockStart + ": " + i18n.Text("updater.labels.env_block_header") + "\n")
	for _, k := range managedKeys() {
		if v, ok := values[k]; ok && v != "" {
			b.WriteString(k + "=" + v + "\n")
		}
	}
	b.WriteString(blockEnd + "\n")
	return b.String(), disabled, nil
}

func outsideBlock(raw string, disable map[string]bool) (lines, disabled []string) {
	in := false
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, blockStart):
			in = true
			continue
		case in && strings.HasPrefix(line, blockEnd):
			in = false
			continue
		case in:
			continue
		}
		if m := assignment.FindStringSubmatch(line); len(m) > 1 && disable[m[1]] {
			disabled = append(disabled, m[1])
			line = "# " + line + "  # " + i18n.Text("updater.labels.env_superseded")
		}
		lines = append(lines, line)
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, disabled
}

// Keep 0600: the file holds the JWT secret.
func updateEnvFile(path, version string, values map[string]string) ([]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // this install's own deploy/.env
	if err != nil && !os.IsNotExist(err) {
		return nil, apperr.Wrap(err, "updater.errors.read_path", "path", path)
	}
	text := setVersionLine(string(raw), version)
	out, disabled, err := writeBlock(text, values)
	if err != nil {
		return nil, err
	}
	if err := fsx.WriteFile(path, []byte(out), 0o600); err != nil {
		return nil, apperr.Wrap(err, "updater.errors.write_path", "path", path)
	}
	return disabled, nil
}

// OCTOPUS_VERSION stays outside the block: it is the operator's setting that `thulu apply`
// converges on.
func setVersionLine(raw, version string) string {
	if version == "" {
		return raw
	}
	lines := strings.Split(raw, "\n")
	in := false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, blockStart):
			in = true
		case strings.HasPrefix(line, blockEnd):
			in = false
		case !in:
			if m := assignment.FindStringSubmatch(line); len(m) > 1 && m[1] == envVersion {
				lines[i] = envVersion + "=" + version
				return strings.Join(lines, "\n")
			}
		}
	}
	raw = strings.TrimRight(raw, "\n")
	if raw != "" {
		raw += "\n"
	}
	return raw + envVersion + "=" + version + "\n"
}

const envVersion = "OCTOPUS_VERSION"
