package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

type Install struct {
	Root        string
	Mode        initwizard.Mode
	ComposeFile string
	Release     string
	Namespace   string
	DaemonURL   string
	DevToken    string
	APIKey      string

	RunningVersion string
}

func (i *Install) DevStack() bool {
	return strings.HasSuffix(i.ComposeFile, "docker-compose.dev.yml")
}

func (i *Install) PublishesPostgres() bool {
	if i.ComposeFile == "" {
		return false
	}
	root, err := os.OpenRoot(i.Root)
	if err != nil {
		return false
	}
	defer root.Close()
	body, err := root.ReadFile(i.ComposeFile)
	if err != nil {
		return false
	}
	return strings.Contains(string(body), hostPostgresPublish) || strings.Contains(string(body), fixedPostgresPublish)
}

// Must match the pg ports entry in the shipped deploy/docker-compose*.yml stacks. The fixed form is
// the dev stack's, and every stack's before OCTOPUS_PG_HOST_PORT.
const (
	hostPostgresPublish  = `"127.0.0.1:${OCTOPUS_PG_HOST_PORT:-5433}:5432"`
	fixedPostgresPublish = `"127.0.0.1:5433:5432"`
)

func (i *Install) HasSource() bool {
	for _, name := range []string{"go.mod", "main.go"} {
		if _, err := os.Stat(filepath.Join(i.Root, name)); err != nil {
			return false
		}
	}
	return true
}

func Detect(ctx context.Context, root string) (*Install, error) {
	answers, err := initwizard.LoadAnswers(root)
	if err != nil {
		return nil, err
	}
	in := &Install{
		Root:        root,
		Mode:        answers.Mode,
		ComposeFile: answers.ComposeFile,
		Release:     answers.Release,
		Namespace:   answers.Namespace,
	}
	if in.Mode == "" {
		in.Mode = initwizard.ModeDocker
	}
	if in.Mode == initwizard.ModeDocker && in.ComposeFile == "" {
		in.ComposeFile = detectComposeFile(ctx, root)
	}
	if in.Release == "" {
		in.Release = "octopus"
	}
	if in.Namespace == "" {
		in.Namespace = "octopus"
	}
	in.DaemonURL = daemonURL(answers)
	in.DevToken = initwizard.InstallDevToken(root)
	in.APIKey = strings.TrimSpace(in.Getenv()("OCTOPUS_API_KEY"))
	in.RunningVersion = in.Running(ctx)
	return in, nil
}

func detectComposeFile(ctx context.Context, root string) string {
	candidates := []string{
		"deploy/docker-compose.dev.yml",
		"deploy/docker-compose.standalone.yml",
		"deploy/docker-compose.caddy.yml",
		"deploy/docker-compose.yml",
	}
	for _, file := range candidates {
		//nolint:gosec // fixed argv; `file` comes from the candidate list above
		cmd := exec.CommandContext(ctx, "docker", "compose", "-f", file, "ps", "-q")
		cmd.Dir = root
		out, err := cmd.Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return file
		}
	}
	return "deploy/docker-compose.yml"
}

func daemonURL(answers *initwizard.Config) string {
	if answers.Mode == initwizard.ModeKubernetes && answers.Domain != "" {
		return "https://" + answers.Domain
	}
	return "http://localhost:8585"
}

func (i *Install) Running(ctx context.Context) string {
	return runningVersion(ctx, i.DaemonURL, i.DevToken, i.APIKey)
}

func runningVersion(ctx context.Context, baseURL, devToken, apiKey string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/info", http.NoBody)
	if err != nil {
		return ""
	}
	attachAuth(req, devToken, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return ""
	}
	return info.Version
}

func (i *Install) EnvPath() string { return filepath.Join(i.Root, "deploy", ".env") }

func (i *Install) BackupScript() string { return filepath.Join(i.Root, "deploy", "backup.sh") }

func (i *Install) HasBackupScript() bool {
	info, err := os.Stat(i.BackupScript())
	return err == nil && !info.IsDir()
}

func (i *Install) String() string {
	switch {
	case i.Mode == initwizard.ModeKubernetes:
		return i18n.Text("updater.labels.install_kubernetes", "release", i.Release, "namespace", i.Namespace)
	case i.ComposeFile != "":
		return i18n.Text("updater.labels.install_docker", "compose", i.ComposeFile)
	case i.Mode != "":
		return string(i.Mode)
	}
	return i18n.Text("updater.labels.install_unrecognized")
}
