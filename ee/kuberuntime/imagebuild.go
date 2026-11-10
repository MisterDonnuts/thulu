// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"log"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// The published agent images carry no copy of Claude Code (images/snippets/claude-first-run.sh
// installs it on first use), and in Kubernetes every agent pod has its own home, so every pod would
// download it again. The image builder derives, once per install, an image that installs Claude
// Code at build time: a BuildKit Job in its own namespace builds FROM the published image and pushes
// to a registry the nodes pull from. Until that image is in the registry, or when the build fails,
// pods keep the published image and its first-run install.
const (
	EnvImageBuild             = "OCTOPUS_AGENT_IMAGE_BUILD"
	EnvImageBuildNamespace    = "OCTOPUS_AGENT_IMAGE_BUILD_NAMESPACE"
	EnvImageBuildPushRegistry = "OCTOPUS_AGENT_IMAGE_BUILD_PUSH_REGISTRY"
	EnvImageBuildPullRegistry = "OCTOPUS_AGENT_IMAGE_BUILD_PULL_REGISTRY"
	EnvImageBuildInsecure     = "OCTOPUS_AGENT_IMAGE_BUILD_INSECURE"
	EnvImageBuildAuthSecret   = "OCTOPUS_AGENT_IMAGE_BUILD_AUTH_SECRET"
	EnvImageBuildBuilderImage = "OCTOPUS_AGENT_IMAGE_BUILD_BUILDER_IMAGE"
)

// Labels images/kube-agent/Dockerfile writes; an image without them is left alone.
const (
	LabelClaudeCodeInstall = "io.thulu.claude-code.install"
	LabelClaudeCodeVersion = "io.thulu.claude-code.version"
	claudeInstallFirstRun  = "first-run"
)

const (
	ImageBuildPending  = "pending"
	ImageBuildBuilding = "building"
	ImageBuildReady    = "ready"
	ImageBuildFailed   = "failed"
	ImageBuildSkipped  = "skipped"
)

const (
	imageBuildRetry   = 30 * time.Minute
	imageBuildTimeout = 30 * time.Minute
)

type ImageBuildConfig struct {
	Namespace string
	// Where the Job pushes and the daemon checks for the image, e.g.
	// registry.octopus-builder.svc.cluster.local:5000.
	PushRegistry string
	// What agent pods name: localhost:<port> for the bundled registry's node mirror, the push
	// registry otherwise.
	PullRegistry string
	// Plain HTTP to PushRegistry (the bundled registry).
	Insecure bool
	// A kubernetes.io/dockerconfigjson Secret in Namespace, for pushing and reading the registry.
	AuthSecret   string
	BuilderImage string
}

func (c ImageBuildConfig) enabled() bool {
	return c.Namespace != "" && c.PushRegistry != "" && c.BuilderImage != ""
}

func imageBuildFromEnv(env func(string) string) ImageBuildConfig {
	if !truthy(env(EnvImageBuild)) {
		return ImageBuildConfig{}
	}
	c := ImageBuildConfig{
		Namespace:    env(EnvImageBuildNamespace),
		PushRegistry: strings.TrimSuffix(env(EnvImageBuildPushRegistry), "/"),
		PullRegistry: strings.TrimSuffix(env(EnvImageBuildPullRegistry), "/"),
		Insecure:     truthy(env(EnvImageBuildInsecure)),
		AuthSecret:   env(EnvImageBuildAuthSecret),
		BuilderImage: env(EnvImageBuildBuilderImage),
	}
	if c.PullRegistry == "" {
		c.PullRegistry = c.PushRegistry
	}
	return c
}

// ImageBuildStatus is one base image and what became of it. Err carries the reason for failed and
// skipped; Image is the ref agent pods use once ready.
type ImageBuildStatus struct {
	Base  string
	Image string
	State string
	Job   string
	Err   error
	Since time.Time
	// Where the Job pushes; Image is the same image as agent pods name it.
	Target string
	// The base pinned by digest, the build's FROM.
	from string
}

type imageBuilder struct {
	rt   *Runtime
	cfg  ImageBuildConfig
	reg  imageRegistry
	poll time.Duration
	now  func() time.Time

	mu     sync.RWMutex
	built  map[string]string
	status map[string]ImageBuildStatus
	order  []string
}

func newImageBuilder(rt *Runtime, cfg ImageBuildConfig) *imageBuilder {
	return &imageBuilder{
		rt: rt, cfg: cfg, reg: ggcrRegistry{insecureHost: insecureHost(cfg)},
		poll: 5 * time.Second, now: time.Now,
		built: map[string]string{}, status: map[string]ImageBuildStatus{},
	}
}

func insecureHost(cfg ImageBuildConfig) string {
	if !cfg.Insecure {
		return ""
	}
	host, _, _ := strings.Cut(cfg.PushRegistry, "/")
	return host
}

// The images an agent pod runs Claude Code from: the combined override, or the claude-code
// engine's image.
func (b *imageBuilder) bases() []string {
	imgs := agentimage.Images{Override: b.rt.cfg.Image, ByEngine: b.rt.cfg.EngineImages}
	out := []string{}
	if img, _ := imgs.For("claude-code"); img != "" {
		out = append(out, img)
	}
	return out
}

func (b *imageBuilder) lookup(image string) (string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ref, ok := b.built[image]
	return ref, ok
}

func (b *imageBuilder) set(s ImageBuildStatus) {
	s.Since = b.now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.status[s.Base]; !ok {
		b.order = append(b.order, s.Base)
	}
	b.status[s.Base] = s
	if s.State == ImageBuildReady {
		b.built[s.Base] = s.Image
	}
}

func (b *imageBuilder) snapshot() []ImageBuildStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]ImageBuildStatus, 0, len(b.order))
	for _, base := range b.order {
		out = append(out, b.status[base])
	}
	return out
}

// run settles every base image, then retries the failed ones every imageBuildRetry until ctx ends.
func (b *imageBuilder) run(ctx context.Context) {
	bases := b.bases()
	for _, base := range bases {
		b.set(ImageBuildStatus{Base: base, State: ImageBuildPending})
	}
	for {
		failed := false
		for _, base := range bases {
			if _, ok := b.lookup(base); ok {
				continue
			}
			if s := b.ensure(ctx, base); s.State == ImageBuildFailed {
				failed = true
			}
		}
		if !failed {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(imageBuildRetry):
		}
	}
}

func (b *imageBuilder) ensure(ctx context.Context, base string) ImageBuildStatus {
	s := b.plan(ctx, base)
	if s.State != ImageBuildBuilding {
		b.finish(s)
		return s
	}
	b.set(s)
	log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.image_build_started", "base", base, "image", s.Target, "job", s.Job))
	start := b.now()
	if err := b.build(ctx, s); err != nil {
		s.State, s.Err = ImageBuildFailed, err
		b.finish(s)
		return s
	}
	s.State = ImageBuildReady
	b.finish(s)
	log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.image_build_done", "image", s.Image,
		"seconds", int(b.now().Sub(start).Seconds())))
	return s
}

func (b *imageBuilder) finish(s ImageBuildStatus) {
	b.set(s)
	switch s.State {
	case ImageBuildReady:
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.image_build_ready", "base", s.Base, "image", s.Image))
	case ImageBuildSkipped:
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.image_build_skipped", "base", s.Base,
			"reason", s.Err))
	case ImageBuildFailed:
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.image_build_failed", "base", s.Base,
			"reason", s.Err))
	}
}

// plan decides what to do with base: skip it, use an image already built, or build one (state
// building, with Target and Job filled in).
func (b *imageBuilder) plan(ctx context.Context, base string) ImageBuildStatus {
	s := ImageBuildStatus{Base: base}
	if len(b.rt.cfg.ForwardEnv) > 0 || len(b.rt.cfg.CABundle) > 0 {
		s.State, s.Err = ImageBuildSkipped, apperr.Precondition("kuberuntime.image_build.proxy_or_ca")
		return s
	}
	auth, err := b.credentials(ctx)
	if err != nil {
		s.State, s.Err = ImageBuildFailed, err
		return s
	}
	info, err := b.reg.base(ctx, b.readRef(base), auth)
	if err != nil {
		s.State, s.Err = ImageBuildSkipped, apperr.Wrap(err, "kuberuntime.image_build.base_unreadable", "image", base)
		return s
	}
	switch info.labels[LabelClaudeCodeInstall] {
	case claudeInstallFirstRun:
	case "":
		s.State, s.Err = ImageBuildSkipped, apperr.Precondition("kuberuntime.image_build.no_label", "image", base)
		return s
	default:
		s.State, s.Err = ImageBuildSkipped, apperr.Precondition("kuberuntime.image_build.bundled", "image", base)
		return s
	}
	target := targetName(base, info)
	s.Target = b.cfg.PushRegistry + "/" + target
	s.Image = b.cfg.PullRegistry + "/" + target
	ok, err := b.reg.exists(ctx, s.Target, auth)
	if err != nil {
		s.State, s.Err = ImageBuildFailed, apperr.Wrap(err, "kuberuntime.image_build.registry_unreachable",
			"registry", b.cfg.PushRegistry)
		return s
	}
	if ok {
		s.State = ImageBuildReady
		return s
	}
	s.State, s.Job, s.from = ImageBuildBuilding, imageBuildJobName(s.Target), info.pinned
	return s
}

// readRef is where the daemon and BuildKit read base from. A base kept in the bundled registry is
// named through the node mirror (localhost:<hostPort>/...), which only the nodes reach; the daemon
// and the Job read the same image through the registry's Service.
func (b *imageBuilder) readRef(base string) string {
	pull, push := b.cfg.PullRegistry, b.cfg.PushRegistry
	if pull == "" || pull == push || !strings.HasPrefix(base, pull+"/") {
		return base
	}
	return push + strings.TrimPrefix(base, pull)
}

var tagUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// <daemon version>-cc<Claude Code version>-<12 hex of the base digest>: a new release, a new
// Claude Code pin or a re-pushed base each get their own image.
func targetName(base string, info baseImage) string {
	repo := base
	if at := strings.Index(repo, "@"); at >= 0 {
		repo = repo[:at]
	}
	if c := strings.LastIndex(repo, ":"); c > strings.LastIndex(repo, "/") {
		repo = repo[:c]
	}
	digest := strings.TrimPrefix(info.digest, "sha256:")
	if len(digest) > 12 {
		digest = digest[:12]
	}
	tag := tagUnsafe.ReplaceAllString(buildinfo.Version, "_") + "-cc" +
		tagUnsafe.ReplaceAllString(info.labels[LabelClaudeCodeVersion], "_") + "-" + digest
	tag = strings.TrimLeft(tag, ".-")
	if len(tag) > 128 {
		tag = tag[:128]
	}
	return path.Base(repo) + ":" + tag
}

// StartImageBuild builds the agent image in the background; a no-op when the build is off. Call it
// once, from the daemon holding the singleton lease.
func (r *Runtime) StartImageBuild(ctx context.Context) {
	if r.images != nil {
		go r.images.run(ctx)
	}
}

// ImageBuilds is nil when the build is off.
func (r *Runtime) ImageBuilds() []ImageBuildStatus {
	if r.images == nil {
		return nil
	}
	return r.images.snapshot()
}
