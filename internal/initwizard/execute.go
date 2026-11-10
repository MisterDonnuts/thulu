package initwizard

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const devOverrideFile = "deploy/docker-compose.nologin.yml"

const (
	prodComposeFile       = "deploy/docker-compose.yml"
	devComposeFile        = "deploy/docker-compose.dev.yml"
	standaloneComposeFile = "deploy/docker-compose.standalone.yml"
	caddyComposeFile      = "deploy/docker-compose.caddy.yml"
)

func behindCaddy(file string) bool {
	return file == prodComposeFile || file == caddyComposeFile
}

// Must match IMAGE_PLATFORMS in .github/workflows/octopus-release.yml.
var publishedImageArchs = []string{"amd64", "arm64"}

func imagePublishedFor(arch string) bool { return slices.Contains(publishedImageArchs, arch) }

func publishedImageArchList() string { return strings.Join(publishedImageArchs, ", ") }

const DefaultRepo = "MisterDonnuts/thulu"

const (
	ImageRegistryEnv = "OCTOPUS_IMAGE_REGISTRY"
	ReleaseRepoEnv   = "OCTOPUS_RELEASE_REPO"
)

func ReleaseRepo() string {
	if repo := strings.TrimSpace(os.Getenv(ReleaseRepoEnv)); repo != "" {
		return repo
	}
	return DefaultRepo
}

func imageRegistryOverride() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(ImageRegistryEnv)), "/")
}

// Default must match what the release workflow pushes; ghcr.io rejects a mixed-case owner.
func ImageRegistry(repo string) string {
	if reg := imageRegistryOverride(); reg != "" {
		return reg
	}
	owner, _, _ := strings.Cut(repo, "/")
	return "ghcr.io/" + strings.ToLower(owner)
}

func ImageTag(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || version == "dev" {
		return "latest"
	}
	return version
}

func ReleaseImage(version string) string {
	return ReleaseImageFor(ReleaseRepo(), version)
}

func ReleaseImageFor(repo, version string) string {
	return DaemonImageIn(ImageRegistry(repo), version)
}

func DaemonImageIn(registry, version string) string {
	return strings.TrimRight(registry, "/") + "/octopus:" + ImageTag(version)
}

func splitDaemonImage(ref string) (registry, tag string, ok bool) {
	ref = strings.TrimSpace(ref)
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if slash <= 0 || colon < slash || strings.Contains(ref, "@") {
		return "", "", false
	}
	if ref[slash+1:colon] != "octopus" {
		return "", "", false
	}
	return ref[:slash], ref[colon+1:], true
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type ExecRunner struct {
	Dir    string
	Out    io.Writer
	In     io.Reader
	ErrOut io.Writer
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the caller passes a fixed argv (docker/helm), not shell input
	cmd.Dir = r.Dir
	cmd.Stdin = r.In
	cmd.Stdout = r.Out
	cmd.Stderr = r.Out
	if r.ErrOut != nil {
		cmd.Stderr = r.ErrOut
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// Dev mode disables every auth check, so this loopback publish is the install's only access
// control.
func WriteDevOverride(root string) (string, error) {
	// Compose appends ports from overrides instead of replacing them; without !override 8585 is
	// bound twice and fails "address already in use".
	//i18n:ignore compose YAML; the comment above it comes from the catalog
	const services = `services:
  api:
    ports: !override
      - "127.0.0.1:8585:8585"
  web:
    ports: !override
      - "127.0.0.1:4321:4321"
`
	body := yamlComment(i18n.Text("initwizard.files.dev_override_header")) + services
	if err := writePrivate(filepath.Join(root, devOverrideFile), []byte(body)); err != nil {
		return "", apperr.Wrap(err, "initwizard.errors.write_file", "path", devOverrideFile)
	}
	return devOverrideFile, nil
}

func yamlComment(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		b.WriteString(strings.TrimRight("# "+line, " ") + "\n")
	}
	return b.String()
}

// No --build for a published image: the standalone file has no build: key and compose fails on the
// missing context instead of pulling.
func ComposeArgs(cfg *Config, overrides []string) []string {
	args := []string{"compose", "-f", cfg.ComposeFile}
	for _, o := range overrides {
		args = append(args, "-f", o)
	}
	args = append(args, "up", "-d")
	if cfg.Image == "" {
		args = append(args, "--build")
	}
	return args
}

func WriteHelmValues(root string, cfg *Config) (string, error) {
	body, err := yaml.Marshal(cfg.HelmValues())
	if err != nil {
		return "", apperr.Wrap(err, "initwizard.errors.render_values")
	}
	header := []byte(yamlComment(i18n.Text("initwizard.files.helm_values_header")))
	name := fmt.Sprintf("deploy/values-%s.yaml", cfg.Release)
	if err := writePrivate(filepath.Join(root, name), append(header, body...)); err != nil {
		return "", apperr.Wrap(err, "initwizard.errors.write_file", "path", name)
	}
	return name, nil
}

func LoadHelmSecrets(root, release string) (jwt, secretboxKey string) {
	f := loadHelmEnv(root, release)
	if f == nil {
		return "", ""
	}
	return f.Get("OCTOPUS_JWT_SECRET"), f.Get("OCTOPUS_SECRETBOX_KEY")
}

func loadHelmEnv(root, release string) *EnvFile {
	// os.Root: the release name is operator-typed and "../x" must not read outside deploy/.
	dir, err := os.OpenRoot(filepath.Join(root, "deploy"))
	if err != nil {
		return nil
	}
	defer dir.Close()
	body, err := dir.ReadFile("values-" + release + ".yaml")
	if err != nil {
		return nil
	}
	var v struct {
		Config struct {
			Domain         string `yaml:"domain"`
			AllowedOrigins string `yaml:"allowedOrigins"`
			TrustedProxies string `yaml:"trustedProxies"`
			SSO            struct {
				AllowedDomains string `yaml:"allowedDomains"`
			} `yaml:"sso"`
		} `yaml:"config"`
		Secrets struct {
			JWTSecret    string `yaml:"jwtSecret"`
			SecretboxKey string `yaml:"secretboxKey"`
			LicenseKey   string `yaml:"licenseKey"`
			OIDC         struct {
				Issuer       string `yaml:"issuer"`
				ClientID     string `yaml:"clientId"`
				ClientSecret string `yaml:"clientSecret"`
				Name         string `yaml:"name"`
				GroupRoleMap string `yaml:"groupRoleMap"`
			} `yaml:"oidc"`
			GitHub struct {
				ClientID     string `yaml:"clientId"`
				ClientSecret string `yaml:"clientSecret"`
			} `yaml:"github"`
		} `yaml:"secrets"`
		ExtraEnv map[string]string `yaml:"extraEnv"`
		GitHub   struct {
			URL    string `yaml:"url"`
			APIURL string `yaml:"apiUrl"`
		} `yaml:"github"`
	}
	if yaml.Unmarshal(body, &v) != nil {
		return nil
	}
	f := &EnvFile{}
	for key, value := range map[string]string{
		"OCTOPUS_DOMAIN":               v.Config.Domain,
		"OCTOPUS_ALLOWED_ORIGINS":      v.Config.AllowedOrigins,
		"OCTOPUS_TRUSTED_PROXIES":      v.Config.TrustedProxies,
		"OCTOPUS_SSO_ALLOWED_DOMAINS":  v.Config.SSO.AllowedDomains,
		"OCTOPUS_JWT_SECRET":           v.Secrets.JWTSecret,
		"OCTOPUS_SECRETBOX_KEY":        v.Secrets.SecretboxKey,
		"OCTOPUS_LICENSE_KEY":          v.Secrets.LicenseKey,
		"OCTOPUS_OIDC_ISSUER":          v.Secrets.OIDC.Issuer,
		"OCTOPUS_OIDC_CLIENT_ID":       v.Secrets.OIDC.ClientID,
		"OCTOPUS_OIDC_CLIENT_SECRET":   v.Secrets.OIDC.ClientSecret,
		"OCTOPUS_OIDC_NAME":            v.Secrets.OIDC.Name,
		"OCTOPUS_OIDC_GROUP_ROLE_MAP":  v.Secrets.OIDC.GroupRoleMap,
		"OCTOPUS_GITHUB_CLIENT_ID":     v.Secrets.GitHub.ClientID,
		"OCTOPUS_GITHUB_CLIENT_SECRET": v.Secrets.GitHub.ClientSecret,
		"OCTOPUS_GITHUB_URL":           v.GitHub.URL,
		"OCTOPUS_GITHUB_API_URL":       v.GitHub.APIURL,
	} {
		if value != "" {
			f.Set(key, value)
		}
	}
	for key, value := range v.ExtraEnv {
		f.Set(key, value)
	}
	return f
}

func HelmArgs(cfg *Config, valuesPath string) []string {
	return []string{
		"upgrade", "--install", cfg.Release, "deploy/helm/octopus",
		"--namespace", cfg.Namespace, "--create-namespace",
		"-f", valuesPath, "--wait", "--timeout", "10m",
	}
}

// A first --build compiles the daemon and installs dashboard deps; minutes is normal.
const readyTimeout = 6 * time.Minute

func WaitForDaemon(ctx context.Context, baseURL string) error {
	return WaitForHTTP(ctx, strings.TrimRight(baseURL, "/")+"/health/ready", 200, readyTimeout, nil)
}
