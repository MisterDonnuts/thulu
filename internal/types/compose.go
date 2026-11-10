package types

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

type ComposeServiceSpec struct {
	Build composeBuild `yaml:"build"`
	Image string       `yaml:"image"`
	// container_name is ignored on purpose: the launcher names containers from INSTANCE_ID
	// (containerNameFor).
	EnvFile     composeEnvFile    `yaml:"env_file"`
	Environment []string          `yaml:"environment"`
	Volumes     []string          `yaml:"volumes"`
	Labels      map[string]string `yaml:"labels"`
	NetworkMode string            `yaml:"network_mode"`
	ShmSize     string            `yaml:"shm_size"`
	StopGrace   string            `yaml:"stop_grace_period"`
	Command     composeCommand    `yaml:"command"`
	Entrypoint  composeCommand    `yaml:"entrypoint"`
}

type composeCommand []string

func (c *composeCommand) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err == nil {
		if s == "" {
			*c = nil
			return nil
		}
		*c = []string{"/bin/sh", "-c", s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*c = list
	return nil
}

type composeBuild struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

func (b *composeBuild) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err == nil {
		b.Context = s
		return nil
	}
	type raw composeBuild
	return value.Decode((*raw)(b))
}

type composeEnvFile []string

func (e *composeEnvFile) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err == nil {
		*e = []string{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*e = list
	return nil
}

type composeFileSpec struct {
	Services map[string]ComposeServiceSpec `yaml:"services"`
}

func ParseComposeSpec(dir string) (*ComposeServiceSpec, string, error) {
	data, err := safepath.ReadFile(dir, "docker-compose.yml")
	if err != nil {
		return nil, "", apperr.Wrap(err, "types.errors.read_compose")
	}

	var cf composeFileSpec
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, "", apperr.WrapKind(apperr.KindInvalid, err, "types.errors.parse_compose")
	}

	for name := range cf.Services {
		svc := cf.Services[name]
		return &svc, name, nil
	}
	return nil, "", apperr.Invalid("types.errors.compose_no_services")
}

func ParseEnvFile(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path is an env_file entry from the operator's own compose spec, resolved against the agent dir
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var result []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		result = append(result, line)
	}
	return result, scanner.Err()
}

var varPattern = regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

func ExpandEnv(s string, lookup func(string) (string, bool)) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		if strings.HasPrefix(match, "${") {
			inner := match[2 : len(match)-1]

			if idx := strings.Index(inner, ":-"); idx >= 0 {
				key := inner[:idx]
				def := inner[idx+2:]
				if val, ok := lookup(key); ok && val != "" {
					return val
				}
				return def
			}

			if val, ok := lookup(inner); ok {
				return val
			}
			return ""
		}

		key := match[1:]
		if val, ok := lookup(key); ok {
			return val
		}
		return ""
	})
}

func ResolveEnvironment(spec *ComposeServiceSpec, dir string, overrides map[string]string) ([]string, error) {
	lookup := envLookup(overrides)

	result := make(map[string]string)
	var order []string
	addVar := func(key, val string) {
		if _, exists := result[key]; !exists {
			order = append(order, key)
		}
		result[key] = val
	}

	if err := applyEnvFiles(spec.EnvFile, dir, addVar); err != nil {
		return nil, err
	}
	for _, entry := range spec.Environment {
		if k, v, ok := strings.Cut(ExpandEnv(entry, lookup), "="); ok {
			addVar(k, v)
		}
	}
	for k, v := range overrides {
		addVar(k, v)
	}

	final := make([]string, 0, len(order))
	for _, k := range order {
		final = append(final, k+"="+result[k])
	}
	return final, nil
}

func envLookup(overrides map[string]string) func(string) (string, bool) {
	envMap := make(map[string]string)
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			envMap[k] = v
		}
	}
	for k, v := range overrides {
		envMap[k] = v
	}
	return func(key string) (string, bool) {
		v, ok := envMap[key]
		return v, ok
	}
}

func applyEnvFiles(envFiles []string, dir string, addVar func(key, val string)) error {
	for _, ef := range envFiles {
		path := ef
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		entries, err := ParseEnvFile(path)
		if err != nil {
			return apperr.Wrap(err, "types.errors.env_file", "file", ef)
		}
		for _, entry := range entries {
			if k, v, ok := strings.Cut(entry, "="); ok {
				addVar(k, v)
			}
		}
	}
	return nil
}

func ParseVolume(v, baseDir string) (source, target string, readOnly bool) {
	parts := strings.SplitN(v, ":", 3)
	if len(parts) < 2 {
		return v, v, false
	}

	source = parts[0]
	target = parts[1]

	if !filepath.IsAbs(source) {
		source = filepath.Join(baseDir, source)
	}
	source = filepath.Clean(source)

	if len(parts) == 3 && parts[2] == "ro" {
		readOnly = true
	}

	return source, target, readOnly
}
