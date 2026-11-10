package initwizard

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
)

type EnvFile struct {
	lines []string
}

func LoadEnvFile(path string) (*EnvFile, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path, by design
	if err != nil {
		if os.IsNotExist(err) {
			return &EnvFile{}, nil
		}
		return nil, apperr.Wrap(err, "initwizard.errors.read_file", "path", path)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return &EnvFile{}, nil
	}
	return &EnvFile{lines: strings.Split(text, "\n")}, nil
}

func (f *EnvFile) Get(key string) string {
	for _, line := range f.lines {
		if k, v, ok := parseAssignment(line); ok && k == key {
			return v
		}
	}
	return ""
}

func (f *EnvFile) Set(key, value string) {
	rendered := key + "=" + quoteEnv(value)
	for i, line := range f.lines {
		if k, _, ok := parseAssignment(line); ok && k == key {
			f.lines[i] = rendered
			return
		}
	}
	for i, line := range f.lines {
		if commentedKey(line) == key {
			f.lines[i] = rendered
			return
		}
	}
	f.lines = append(f.lines, rendered)
}

func (f *EnvFile) Unset(key string) {
	for i, line := range f.lines {
		if k, _, ok := parseAssignment(line); ok && k == key {
			f.lines[i] = "# " + line
			return
		}
	}
}

func (f *EnvFile) Render() string {
	if len(f.lines) == 0 {
		return ""
	}
	return strings.Join(f.lines, "\n") + "\n"
}

// 0600: the file holds the JWT secret and OIDC client secret; 0644 would expose them to every
// account on the host.
func (f *EnvFile) Save(path string) error {
	if err := writePrivate(path, []byte(f.Render())); err != nil {
		return apperr.Wrap(err, "initwizard.errors.write_file", "path", path)
	}
	return nil
}

func parseAssignment(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	key, value, ok = strings.Cut(trimmed, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
	return key, unquoteEnv(strings.TrimSpace(stripInlineComment(value))), true
}

// .env.example ships `OCTOPUS_JWT_SECRET=   # generate with: ...`; without this the comment becomes
// the secret.
// The # must follow whitespace so a DSN password containing one survives.
func stripInlineComment(value string) string {
	trimmed := strings.TrimLeft(value, " \t")
	if strings.HasPrefix(trimmed, `"`) || strings.HasPrefix(trimmed, "'") {
		return value
	}
	for i, r := range value {
		if r != '#' {
			continue
		}
		if i == 0 {
			return ""
		}
		if prev := value[i-1]; prev == ' ' || prev == '\t' {
			return value[:i]
		}
	}
	return value
}

func commentedKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return ""
	}
	body := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	key, _, ok := strings.Cut(body, "=")
	if !ok {
		return ""
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, " \t") {
		return ""
	}
	return key
}

// Compose reads an unquoted # as a comment and drops trailing spaces.
func quoteEnv(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t#\"'") || strings.TrimSpace(value) != value {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	}
	return value
}

func unquoteEnv(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func InstallDevToken(root string) string {
	if tok := strings.TrimSpace(os.Getenv(auth.DevTokenEnv)); tok != "" {
		return tok
	}
	if root == "" {
		return ""
	}
	f, err := LoadEnvFile(filepath.Join(root, "deploy", ".env"))
	if err != nil {
		return ""
	}
	return InstallDevTokenFromEnvFile(f)
}

func InstallDevTokenFromEnvFile(f *EnvFile) string {
	if f.Get("OCTOPUS_DEV_MODE") != "1" {
		return ""
	}
	return f.Get(auth.DevTokenEnv)
}

const (
	// The compose stacks publish pg on this loopback port: OCTOPUS_PG_HOST_PORT, 5433 without it.
	pgHostPortEnv     = "OCTOPUS_PG_HOST_PORT"
	defaultPGHostPort = 5433
)

func bundledHostAddrOn(port string) string {
	if port == "" {
		port = strconv.Itoa(defaultPGHostPort)
	}
	return "localhost:" + port
}

// BundledHostAddr is where the host reaches the bundled Postgres of the install f describes.
func BundledHostAddr(f *EnvFile) string {
	if f == nil {
		return bundledHostAddrOn("")
	}
	return bundledHostAddrOn(f.Get(pgHostPortEnv))
}

func HostDatabaseURL(f *EnvFile) string {
	if f == nil {
		return ""
	}
	dsn := strings.TrimSpace(f.Get("OCTOPUS_DATABASE_URL"))
	if dsn == "" {
		if pw := f.Get("OCTOPUS_PG_PASSWORD"); pw != "" {
			return bundledDSN(pw, BundledHostAddr(f))
		}
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "pg" {
		return dsn
	}
	u.Host = BundledHostAddr(f)
	return u.String()
}

func AdoptEnv(f *EnvFile, keys ...string) error {
	if f == nil {
		return nil
	}
	for _, k := range keys {
		if _, set := os.LookupEnv(k); set {
			continue
		}
		if v := f.Get(k); v != "" {
			if err := os.Setenv(k, v); err != nil {
				return apperr.Wrap(err, "initwizard.errors.adopt_env", "name", k)
			}
		}
	}
	return nil
}
