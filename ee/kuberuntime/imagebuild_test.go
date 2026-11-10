// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const builderNS = "octopus-builder"

// pushBase puts an image with labels at host/repo:tag and returns the ref.
func pushBase(t *testing.T, host, repo string, labels map[string]string) string {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf.Config.Labels = labels
	if img, err = mutate.ConfigFile(img, cf); err != nil {
		t.Fatal(err)
	}
	ref := host + "/" + repo + ":1"
	if err := remote.Write(mustRef(t, ref), img); err != nil {
		t.Fatal(err)
	}
	return ref
}

func mustRef(t *testing.T, ref string) name.Reference {
	t.Helper()
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newTestRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func newBuildRuntime(t *testing.T, base, pushHost string) (*Runtime, *fake.Clientset) {
	t.Helper()
	rt, client := newFakeRuntime(t, Config{
		EngineImages: map[string]string{"claude-code": base, "codex": "example.invalid/agent-codex:1"},
		ImageBuild: ImageBuildConfig{
			Namespace: builderNS, PushRegistry: pushHost + "/agents", PullRegistry: "localhost:30500/agents",
			Insecure: true, BuilderImage: "docker.io/moby/buildkit:v0.23.2-rootless@sha256:abc",
		},
	})
	rt.images.poll = 2 * time.Millisecond
	return rt, client
}

// finishJobs marks every Job in the builder namespace with cond as soon as it appears.
func finishJobs(ctx context.Context, t *testing.T, client *fake.Clientset, cond batchv1.JobConditionType) {
	t.Helper()
	go func() {
		for ctx.Err() == nil {
			list, err := client.BatchV1().Jobs(builderNS).List(ctx, metav1.ListOptions{})
			if err == nil {
				for i := range list.Items {
					j := &list.Items[i]
					if jobFinished(j) != "" {
						continue
					}
					j.Status.Conditions = append(j.Status.Conditions, batchv1.JobCondition{Type: cond, Status: corev1.ConditionTrue})
					_, _ = client.BatchV1().Jobs(builderNS).UpdateStatus(ctx, j, metav1.UpdateOptions{})
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
}

func firstRunLabels() map[string]string {
	return map[string]string{LabelClaudeCodeInstall: "first-run", LabelClaudeCodeVersion: "2.1.280"}
}

func TestImageBuildFromEnv(t *testing.T) {
	env := map[string]string{
		EnvImageBuild:             "true",
		EnvImageBuildNamespace:    builderNS,
		EnvImageBuildPushRegistry: "registry.octopus-builder.svc.cluster.local:5000/",
		EnvImageBuildInsecure:     "true",
		EnvImageBuildBuilderImage: "buildkit@sha256:abc",
	}
	c := imageBuildFromEnv(func(k string) string { return env[k] })
	if !c.enabled() || c.PullRegistry != "registry.octopus-builder.svc.cluster.local:5000" || !c.Insecure {
		t.Errorf("config = %+v", c)
	}
	env[EnvImageBuild] = ""
	if imageBuildFromEnv(func(k string) string { return env[k] }).enabled() {
		t.Error("enabled without OCTOPUS_AGENT_IMAGE_BUILD")
	}
}

func TestTargetName(t *testing.T) {
	info := baseImage{digest: "sha256:0123456789abcdef0123", labels: firstRunLabels()}
	got := targetName("ghcr.io/misterdonnuts/octopus-agent-claude-code:0.3.4@sha256:ffff", info)
	if !strings.HasPrefix(got, "octopus-agent-claude-code:") || !strings.HasSuffix(got, "-cc2.1.280-0123456789ab") {
		t.Errorf("targetName = %q", got)
	}
	if targetName("example.com:5000/a/b", info) != targetName("example.com:5000/a/b:2", info) {
		t.Error("the tag of the base must not change the target: the digest already does")
	}
}

func TestImageBuildSkipsWhatItShouldNotBuild(t *testing.T) {
	host := newTestRegistry(t)
	cases := map[string]struct {
		labels map[string]string
		key    string
	}{
		"bundled":  {map[string]string{LabelClaudeCodeInstall: "build"}, "kuberuntime.image_build.bundled"},
		"no label": {nil, "kuberuntime.image_build.no_label"},
	}
	for label, tc := range cases {
		base := pushBase(t, host, "skip-"+strings.ReplaceAll(label, " ", ""), tc.labels)
		rt, client := newBuildRuntime(t, base, host)
		rt.images.run(context.Background())
		got := rt.ImageBuilds()
		if len(got) != 1 || got[0].State != ImageBuildSkipped || apperr.Code(got[0].Err) != tc.key {
			t.Errorf("%s: status = %+v", label, got)
		}
		if jobs, _ := client.BatchV1().Jobs(builderNS).List(context.Background(), metav1.ListOptions{}); len(jobs.Items) != 0 {
			t.Errorf("%s: started a Job", label)
		}
	}

	rt, _ := newBuildRuntime(t, "127.0.0.1:1/missing:1", host)
	rt.images.run(context.Background())
	if got := rt.ImageBuilds(); got[0].State != ImageBuildSkipped ||
		apperr.Code(got[0].Err) != "kuberuntime.image_build.base_unreadable" {
		t.Errorf("unreadable base: %+v", got)
	}

	base := pushBase(t, host, "proxied", firstRunLabels())
	rt, _ = newBuildRuntime(t, base, host)
	rt.cfg.ForwardEnv = map[string]string{"HTTPS_PROXY": "http://proxy:3128"}
	rt.images.run(context.Background())
	if got := rt.ImageBuilds(); apperr.Code(got[0].Err) != "kuberuntime.image_build.proxy_or_ca" {
		t.Errorf("proxied install: %+v", got)
	}
}

func TestImageBuildRunsTheJobAndSwapsTheImage(t *testing.T) {
	host := newTestRegistry(t)
	base := pushBase(t, host, "octopus-agent-claude-code", firstRunLabels())
	rt, client := newBuildRuntime(t, base, host)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finishJobs(ctx, t, client, batchv1.JobComplete)

	s := rt.images.ensure(ctx, base)
	if s.State != ImageBuildReady || !strings.HasPrefix(s.Image, "localhost:30500/agents/octopus-agent-claude-code:") {
		t.Fatalf("status = %+v", s)
	}
	job, err := client.BatchV1().Jobs(builderNS).Get(ctx, s.Job, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec
	c := pod.Containers[0]
	if c.Image != rt.cfg.ImageBuild.BuilderImage || *pod.AutomountServiceAccountToken ||
		pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeUnconfined {
		t.Errorf("pod spec = %+v", pod)
	}
	if out := strings.Join(c.Args, " "); !strings.Contains(out, "name="+s.Target+",push=true,registry.insecure=true") {
		t.Errorf("args = %s", out)
	}
	cm, err := client.CoreV1().ConfigMaps(builderNS).Get(ctx, s.Job, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if df := cm.Data["Dockerfile"]; !strings.HasPrefix(df, "FROM "+host+"/octopus-agent-claude-code@sha256:") ||
		!strings.Contains(df, "OCTOPUS_ENGINE_DIR=/opt/thulu-engines-image claude --version") {
		t.Errorf("Dockerfile = %s", df)
	}
	if len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].Name != s.Job {
		t.Errorf("the ConfigMap must belong to the Job: %+v", cm.OwnerReferences)
	}

	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ engine, want string }{
		{"claude-code", s.Image},
		{"codex", "example.invalid/agent-codex:1"},
	} {
		spec := baseSpec()
		spec.Image, spec.Engine = "", tc.engine
		spec.InstanceID += string(rune('a' + i))
		podName, err := rt.LaunchPod(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		p, err := client.CoreV1().Pods("tenant-acme").Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Spec.Containers[0].Image; got != tc.want {
			t.Errorf("%s pod image = %q, want %q", tc.engine, got, tc.want)
		}
	}
}

// A base kept in the bundled registry is named through the node mirror; the daemon reads it, and
// the Job builds FROM it, through the registry's Service.
func TestImageBuildReadsABaseInTheBundledRegistryThroughItsService(t *testing.T) {
	host := newTestRegistry(t)
	pushBase(t, host+"/agents", "octopus-agent-claude-code", firstRunLabels())
	base := "localhost:30500/agents/octopus-agent-claude-code:1"
	rt, client := newBuildRuntime(t, base, host)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finishJobs(ctx, t, client, batchv1.JobComplete)

	s := rt.images.ensure(ctx, base)
	if s.State != ImageBuildReady || !strings.HasPrefix(s.Image, "localhost:30500/agents/octopus-agent-claude-code:") ||
		s.Image == base {
		t.Fatalf("status = %+v", s)
	}
	cm, err := client.CoreV1().ConfigMaps(builderNS).Get(ctx, s.Job, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if df := cm.Data["Dockerfile"]; !strings.HasPrefix(df, "FROM "+host+"/agents/octopus-agent-claude-code@sha256:") {
		t.Errorf("Dockerfile = %s", df)
	}
	if got := rt.images.readRef("example.com/agents/x:1"); got != "example.com/agents/x:1" {
		t.Errorf("readRef rewrote a base outside the bundled registry: %s", got)
	}
}

func TestImageBuildUsesAnImageAlreadyBuilt(t *testing.T) {
	host := newTestRegistry(t)
	base := pushBase(t, host, "octopus-agent-claude-code", firstRunLabels())
	rt, client := newBuildRuntime(t, base, host)
	plan := rt.images.plan(context.Background(), base)
	img, _ := remote.Image(mustRef(t, base))
	if err := remote.Write(mustRef(t, plan.Target), img); err != nil {
		t.Fatal(err)
	}
	rt.images.run(context.Background())
	if got, ok := rt.images.lookup(base); !ok || got != plan.Image {
		t.Errorf("lookup = %q %v, want %q", got, ok, plan.Image)
	}
	if jobs, _ := client.BatchV1().Jobs(builderNS).List(context.Background(), metav1.ListOptions{}); len(jobs.Items) != 0 {
		t.Error("started a Job for an image the registry already has")
	}
}

func TestImageBuildFailureKeepsThePublishedImage(t *testing.T) {
	host := newTestRegistry(t)
	base := pushBase(t, host, "octopus-agent-claude-code", firstRunLabels())
	rt, client := newBuildRuntime(t, base, host)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finishJobs(ctx, t, client, batchv1.JobFailed)

	s := rt.images.ensure(ctx, base)
	if s.State != ImageBuildFailed || apperr.Code(s.Err) != "kuberuntime.image_build.job_failed" {
		t.Fatalf("status = %+v", s)
	}
	if _, ok := rt.images.lookup(base); ok {
		t.Error("a failed build must leave pods on the published image")
	}

	// The next attempt replaces the finished Job instead of waiting on it.
	cancel()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	finishJobs(ctx2, t, client, batchv1.JobComplete)
	if s = rt.images.ensure(ctx2, base); s.State != ImageBuildReady {
		t.Errorf("retry: %+v", s)
	}
}

func TestDockerConfigResolve(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("thulu:s3cret"))
	d, err := parseDockerConfig([]byte(`{"auths":{"https://registry.example.com":{"auth":"` + auth + `"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := name.NewRegistry("registry.example.com")
	a, err := d.Resolve(reg)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := a.Authorization()
	if err != nil || cfg.Username != "thulu" || cfg.Password != "s3cret" {
		t.Errorf("auth = %+v, %v", cfg, err)
	}
	other, _ := name.NewRegistry("ghcr.io")
	if a, _ = d.Resolve(other); a != authn.Anonymous {
		t.Error("a registry the Secret does not list must stay anonymous")
	}
}
