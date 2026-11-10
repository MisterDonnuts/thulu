package types

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-units"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/catrust"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/enginecache"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

func launchSpec(agentType AgentType, eng string) (*ComposeServiceSpec, error) {
	if agentType.Dir != "" {
		spec, _, err := ParseComposeSpec(agentType.Dir)
		if err != nil {
			return nil, apperr.Wrap(err, "types.errors.parse_compose_spec")
		}
		return spec, nil
	}
	image := strings.TrimSpace(agentType.Image)
	if image == "" {
		var env string
		if image, env = agentimage.FromEnv(os.Getenv).For(eng); image == "" {
			return nil, apperr.Invalid("types.errors.agent_without_image", "agent", agentType.Name, "env", env)
		}
	}
	return &ComposeServiceSpec{
		Image: image,
		// No volumes or env_file: both are relative to an agent dir that does not exist. A stored
		// agent gets its identity from GET /run/bundle and its env from the dispatcher.
		Labels: map[string]string{"thulu.agent": "true", "thulu.type": agentType.Type},
	}, nil
}

func rootlessAgents() bool { return truthyEnv("OCTOPUS_ROOTLESS_AGENTS") }

func truthyEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func Launch(ctx context.Context, docker *client.Client, agentType AgentType, params LaunchParams) (string, error) {
	eng := params.Env["ENGINE"]
	if eng == "" {
		eng = agentType.Engine
	}
	spec, err := launchSpec(agentType, eng)
	if err != nil {
		return "", err
	}

	imageName, err := EnsureImage(ctx, docker, spec, agentType, params.NoBuild)
	if err != nil {
		return "", apperr.Wrap(err, "types.errors.ensure_image")
	}

	env, err := ResolveEnvironment(spec, agentType.Dir, params.Env)
	if err != nil {
		return "", apperr.Wrap(err, "types.errors.resolve_environment")
	}
	env = runtimeEnv(env)
	caBundle, err := catrust.Load()
	if err != nil {
		return "", apperr.Wrap(err, "types.errors.extra_ca_bundle")
	}

	containerName := containerNameFor(agentType, params)
	labels := buildLaunchLabels(spec, params)

	mounts, copyIns, err := launchMounts(spec, agentType, params)
	if err != nil {
		return "", err
	}
	mounts, env, err = withEngineCache(ctx, docker, params.EngineCacheScope, mounts, env)
	if err != nil {
		return "", err
	}

	hostConfig, err := agentHostConfig(ctx, docker, spec, agentType.Name, mounts, params.Resources)
	if err != nil {
		return "", err
	}
	hostConfig.LogConfig = dockerx.ContainerLogConfig(ctx, docker)
	dockerx.ApplyInit(ctx, docker, imageName, spec.Entrypoint, hostConfig)
	containerConfig := buildContainerConfig(imageName, env, labels, spec)

	resp, err := docker.ContainerCreate(ctx,
		containerConfig,
		hostConfig,
		nil,
		nil,
		containerName,
	)
	if err != nil {
		return "", apperr.Wrap(err, "types.errors.container_create")
	}

	// Copied in before start in both modes: the CA is a daemon-side file a bind mount cannot reach
	// when the daemon is containerized.
	if len(caBundle) > 0 {
		if caErr := copyCABundle(ctx, docker, resp.ID, caBundle); caErr != nil {
			logx.Warn(
				docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}),
				"types.log.remove_after_ca_failure", "container", resp.ID,
			)
			return "", caErr
		}
	}

	if params.UseCopyMode {
		// The entrypoint writes to the runs dir before its own mkdir, so it must exist before
		// start.
		if mkErr := mkdirInContainer(ctx, docker, resp.ID, runsTarget); mkErr != nil {
			logx.Warn(
				docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}),
				"types.log.remove_after_runs_dir_failure", "container", resp.ID,
			)
			return "", apperr.Wrap(mkErr, "types.errors.create_runs_dir")
		}
		if copyErr := applyCopyIns(ctx, docker, resp.ID, copyIns); copyErr != nil {
			logx.Warn(
				docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}),
				"types.log.remove_after_copy_in_failure", "container", resp.ID,
			)
			return "", copyErr
		}
	}

	if err := docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		logx.Warn(
			docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}),
			"types.log.remove_after_start_failure", "container", resp.ID,
		)
		return "", apperr.Wrap(err, "types.errors.container_start")
	}

	shortID := resp.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}

	log.Printf("[launch] %s", i18n.Log("types.log.container_started", "id", shortID, "name", containerName, "image", imageName))
	return shortID, nil
}

func containerNameFor(agentType AgentType, params LaunchParams) string {
	if name := SanitizeContainerName(params.Env["INSTANCE_ID"]); name != "" {
		return name
	}
	return SanitizeContainerName(agentType.Name)
}

func buildLaunchLabels(spec *ComposeServiceSpec, params LaunchParams) map[string]string {
	lookup := func(key string) (string, bool) {
		if v, ok := params.Env[key]; ok {
			return v, true
		}
		v := os.Getenv(key)
		return v, v != ""
	}
	labels := make(map[string]string, len(spec.Labels)+len(params.WorkspacePaths)+3)
	for k, v := range spec.Labels {
		labels[k] = ExpandEnv(v, lookup)
	}
	for name, path := range params.WorkspacePaths {
		labels["thulu.workspace-worktree."+name] = path
	}
	// Names must match agent.Label* constants (literals here to avoid an import cycle).
	// thulu.instance is how Reconcile after a restart and copy-mode output retrieval identify the
	// run; without it tasks stay running forever.
	for envKey, labelKey := range map[string]string{
		"TASK_ID":      "thulu.task",
		"PROJECT_ID":   "thulu.project_id",
		"STAGE":        "thulu.stage",
		"WORKSPACE_ID": "thulu.workspace_id",
		"INSTANCE_ID":  "thulu.instance",
		// Usage reads a run's log with the parser of this engine; without the label every run is read
		// as Claude Code and records nothing.
		"ENGINE": "thulu.engine",
	} {
		if v := params.Env[envKey]; v != "" {
			labels[labelKey] = v
		}
	}
	for k, v := range params.LabelOverrides {
		labels[k] = v
	}
	// Last, so no override can claim another install's container.
	dockerx.StampInstall(labels)
	return labels
}

func namedVolumeMounts(vols []ExtraMount) []mount.Mount {
	out := make([]mount.Mount, 0, len(vols))
	for _, v := range vols {
		if v.Source == "" || v.Target == "" {
			continue
		}
		out = append(out, mount.Mount{Type: mount.TypeVolume, Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
	}
	return out
}

func buildLaunchMounts(spec *ComposeServiceSpec, agentType AgentType, params LaunchParams) ([]mount.Mount, error) {
	mounts := make([]mount.Mount, 0, len(spec.Volumes)+len(params.WorkspacePaths))
	for _, v := range spec.Volumes {
		src, tgt, ro := ParseVolume(v, agentType.Dir)
		if !ro {
			if mkErr := os.MkdirAll(src, 0o750); mkErr != nil {
				return nil, apperr.Wrap(mkErr, "types.errors.create_volume_source", "path", src)
			}
		}
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: src, Target: tgt, ReadOnly: ro})
	}

	for name, path := range params.WorkspacePaths {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   path,
			Target:   "/workspace/projects/" + name,
			ReadOnly: params.WorkspaceReadOnly,
		})
	}

	for name, path := range params.StagePaths {
		if path == "" {
			continue
		}
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   path,
			Target:   "/workspace/projects/" + name + "/.thulu",
			ReadOnly: true,
		})
	}

	for _, em := range params.ExtraMounts {
		if em.Source == "" || em.Target == "" {
			continue
		}
		if !em.ReadOnly {
			if mkErr := os.MkdirAll(em.Source, 0o750); mkErr != nil {
				return nil, apperr.Wrap(mkErr, "types.errors.create_extra_mount_source", "path", em.Source)
			}
		}
		mounts = append(mounts, em.DockerMount(""))
	}
	return mounts, nil
}

func buildHostConfig(spec *ComposeServiceSpec, mounts []mount.Mount, caps ResourceCaps) (*container.HostConfig, error) {
	hostConfig := &container.HostConfig{
		// NetworkMode is set only by applyAgentNetwork, which enforces the host-network opt-in.
		Mounts:      mounts,
		SecurityOpt: []string{"no-new-privileges:true"}, //i18n:ignore Docker security option
	}
	if spec.ShmSize != "" {
		shmBytes, shmErr := units.RAMInBytes(spec.ShmSize)
		if shmErr != nil {
			return nil, apperr.WrapKind(apperr.KindInvalid, shmErr, "types.errors.parse_shm_size", "value", spec.ShmSize)
		}
		hostConfig.ShmSize = shmBytes
	}
	applyResourceCaps(hostConfig, caps)
	return hostConfig, nil
}

func applyResourceCaps(hc *container.HostConfig, caps ResourceCaps) {
	if caps.MemoryBytes > 0 {
		hc.Memory = caps.MemoryBytes
		// Pinning memory+swap to the cap keeps a capped container from spilling into host swap.
		hc.MemorySwap = caps.MemoryBytes
	}
	if caps.PidsLimit > 0 {
		limit := caps.PidsLimit
		hc.PidsLimit = &limit
	}
	if caps.NanoCPUs > 0 {
		hc.NanoCPUs = caps.NanoCPUs
	}
}

func buildContainerConfig(imageName string, env []string, labels map[string]string, spec *ComposeServiceSpec) *container.Config {
	cfg := &container.Config{Image: imageName, Env: env, Labels: labels}
	if len(spec.Entrypoint) > 0 {
		cfg.Entrypoint = []string(spec.Entrypoint)
	}
	if len(spec.Command) > 0 {
		cfg.Cmd = []string(spec.Command)
	}
	// Root by default: the entrypoint's root prologue hands the worktrees to node (uid 1000) before
	// dropping to it.
	// octopus-agent declares USER 1000 for Kubernetes; started as that in Docker, the agent cannot
	// write the daemon's checkout.
	if rootlessAgents() {
		cfg.User = "1000:1000"
	} else {
		cfg.User = "0:0"
	}
	if spec.StopGrace != "" {
		seconds := parseGracePeriod(spec.StopGrace)
		cfg.StopTimeout = &seconds
	}
	return cfg
}

type ImageAPI interface {
	imageref.DockerImages
	ImageBuild(ctx context.Context, buildContext io.Reader, options types.ImageBuildOptions) (types.ImageBuildResponse, error)
}

const EnvBuildMissingImages = "OCTOPUS_BUILD_MISSING_IMAGES"

// Never builds by default: a build runs an arbitrary Dockerfile with the daemon's Docker access.
// OCTOPUS_BUILD_MISSING_IMAGES=1 opts back in.
func EnsureImage(ctx context.Context, docker ImageAPI, spec *ComposeServiceSpec, agentType AgentType, noBuild bool) (string, error) {
	if spec.Build.Context == "" && spec.Image == "" {
		return "", apperr.Invalid("types.errors.no_image_or_build", "agent", agentType.Name)
	}
	// Docker image names must be lowercase [a-z0-9._-]; an agent dir like "Opus" otherwise fails
	// with "invalid reference format".
	imageName := spec.Image
	if imageName == "" {
		imageName = fmt.Sprintf("octopus-built-%s:latest", SanitizeImageName(agentType.Name))
	}
	imageName = imageref.Resolve(imageName)

	canBuild := spec.Build.Context != "" && truthyEnv(EnvBuildMissingImages)
	if canBuild && !noBuild {
		return buildImage(ctx, docker, spec, agentType, imageName)
	}
	err := imageref.EnsureLocal(ctx, docker, imageName)
	var missing *imageref.MissingError
	switch {
	case err == nil:
		return imageName, nil
	case errors.As(err, &missing) && canBuild:
		log.Printf("[launch] %s", i18n.Log("types.log.building_missing_image", "image", imageName, "env", EnvBuildMissingImages))
		return buildImage(ctx, docker, spec, agentType, imageName)
	default:
		return "", apperr.Wrap(err, "types.errors.agent", "agent", agentType.Name)
	}
}

func buildImage(ctx context.Context, docker ImageAPI, spec *ComposeServiceSpec, agentType AgentType, imageName string) (string, error) {
	buildCtx := spec.Build.Context
	if !filepath.IsAbs(buildCtx) {
		buildCtx = filepath.Join(agentType.Dir, buildCtx)
	}
	buildCtx = filepath.Clean(buildCtx)

	dockerfile := spec.Build.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile" //i18n:ignore Docker's default file name
	}

	tarBuf, err := createBuildContext(buildCtx)
	if err != nil {
		return "", apperr.Wrap(err, "types.errors.create_build_context")
	}

	log.Printf("[launch] %s", i18n.Log("types.log.building_image", "image", imageName, "dir", buildCtx))
	resp, err := docker.ImageBuild(ctx, tarBuf, types.ImageBuildOptions{
		Tags:       []string{imageName},
		Dockerfile: dockerfile,
		Remove:     true,
	})
	if err != nil {
		return "", apperr.Wrap(err, "types.errors.image_build")
	}
	defer resp.Body.Close()

	// The build does not finish until its output stream is read to completion.
	if err := consumeBuildOutput(resp.Body); err != nil {
		return "", apperr.Wrap(err, "types.errors.image_build_failed")
	}

	return imageName, nil
}

func createBuildContext(dir string) (_ *bytes.Buffer, retErr error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	defer func() {
		if closeErr := tw.Close(); closeErr != nil && retErr == nil {
			retErr = apperr.Wrap(closeErr, "types.errors.finalize_build_context")
		}
	}()

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
			return filepath.SkipDir
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel

		if writeErr := tw.WriteHeader(header); writeErr != nil {
			return writeErr
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path) //nolint:gosec // G122: path is rooted at validated tar source dir; symlink TOCTOU is acceptable for build context
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(tw, f)
		return err
	})

	return buf, err
}

func consumeBuildOutput(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := decoder.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return nil // deliberate: non-JSON output does not mean the build failed
		}
		if msg.Error != "" {
			return fmt.Errorf("%s", msg.Error)
		}
	}
}

func parseGracePeriod(s string) int {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "m") {
		var m int
		if n, err := fmt.Sscanf(s, "%dm", &m); err != nil || n != 1 {
			logx.Warn(err, "types.log.grace_period_unparsable", "value", s)
			return 30
		}
		return m * 60
	}
	if strings.HasSuffix(s, "s") {
		var sec int
		if n, err := fmt.Sscanf(s, "%ds", &sec); err != nil || n != 1 {
			logx.Warn(err, "types.log.grace_period_unparsable", "value", s)
			return 30
		}
		return sec
	}
	return 30
}

var containerNameRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

func SanitizeContainerName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = containerNameRe.ReplaceAllString(name, "-")
	for name != "" && !isAlphanumeric(name[0]) {
		name = name[1:]
	}
	return name
}

func SanitizeImageName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if valid {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := b.String()
	for out != "" && !(isAlphanumeric(out[0])) {
		out = out[1:]
	}
	for out != "" && !(isAlphanumeric(out[len(out)-1])) {
		out = out[:len(out)-1]
	}
	return out
}

func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// A path outside root is dropped: no mount is safer than mounting the wrong subtree into an agent.
func VolumeSubpaths(what, root string, paths map[string]string) map[string]string {
	if len(paths) == 0 {
		return nil
	}
	out := make(map[string]string, len(paths))
	for name, abs := range paths {
		rel, err := filepath.Rel(root, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			log.Printf("[%s] %s", what, i18n.Log("types.log.worktree_outside_data_root", "name", name, "path", abs, "root", root))
			continue
		}
		out[name] = rel
	}
	return out
}

// launchMounts is every mount but the engine volume, and what copy mode copies in instead of
// binding.
func launchMounts(spec *ComposeServiceSpec, agentType AgentType, params LaunchParams) ([]mount.Mount, []copyIn, error) {
	var (
		mounts  []mount.Mount
		copyIns []copyIn
	)
	if params.UseCopyMode {
		mounts = buildSocketMounts(spec)
		mounts = append(mounts, buildVolumeMounts(params)...)
		copyIns = buildCopyIns(spec, agentType)
	} else {
		var err error
		if mounts, err = buildLaunchMounts(spec, agentType, params); err != nil {
			return nil, nil, err
		}
	}
	return append(mounts, namedVolumeMounts(params.NamedVolumes)...), copyIns, nil
}

// withEngineCache mounts the scope's engine volume and points OCTOPUS_ENGINE_DIR at it; no scope
// leaves both alone.
func withEngineCache(ctx context.Context, api enginecache.API, scope string, mounts []mount.Mount, env []string) ([]mount.Mount, []string, error) {
	if scope == "" {
		return mounts, env, nil
	}
	vol, err := enginecache.Ensure(ctx, api, scope)
	if err != nil {
		return nil, nil, err
	}
	mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: vol, Target: enginecache.MountPath})
	return mounts, withEnv(env, enginecache.EnvDir, enginecache.MountPath), nil
}

// withEnv sets key in a KEY=value list, replacing an earlier value.
func withEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}
