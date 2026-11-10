//go:build !foss

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/errdefs"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/sandbox/execagent"
)

var wantCacheEnv = map[string]string{
	"OCTOPUS_CACHE_DIR": "/cache",
	"npm_config_cache":  "/cache/npm",
	"GOMODCACHE":        "/cache/go/mod",
	"GOCACHE":           "/cache/go/build",
	"PIP_CACHE_DIR":     "/cache/pip",
	"MISE_DATA_DIR":     "/cache/mise",
	"MISE_CACHE_DIR":    "/cache/mise/cache",
	"MISE_YES":          "1",
}

func envMap(env []corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		out[e.Name] = e.Value
	}
	return out
}

func checkCacheEnv(t *testing.T, got map[string]string) {
	t.Helper()
	for k, want := range wantCacheEnv {
		if got[k] != want {
			t.Errorf("env %s = %q, want %q", k, got[k], want)
		}
	}
	if !strings.HasPrefix(got["PATH"], "/cache/mise/shims:") || !strings.Contains(got["PATH"], "/usr/bin") {
		t.Errorf("PATH = %q, want the mise shims first and the system dirs after", got["PATH"])
	}
}

func cacheMounts(c corev1.Container) map[string]corev1.VolumeMount {
	out := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		if m.Name == cacheVolumeName {
			out[m.MountPath] = m
		}
	}
	return out
}

func TestKubeSandboxMountsWorkspaceCache(t *testing.T) {
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img", CacheSize: "30Gi"})
	ctx := context.Background()
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, kuberuntime.CacheClaimName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("cache claim not created: %v", err)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("30Gi")) != 0 {
		t.Errorf("cache claim size = %s, want 30Gi", got.String())
	}

	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == cacheVolumeName {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != kuberuntime.CacheClaimName {
		t.Fatalf("cache volume = %+v, want the %s claim", vol, kuberuntime.CacheClaimName)
	}
	c := pod.Spec.Containers[0]
	mounts := cacheMounts(c)
	root, ok := mounts["/cache"]
	if !ok || root.ReadOnly || root.SubPath != "" {
		t.Errorf("/cache mount = %+v, want the whole claim read-write", root)
	}
	snap, ok := mounts["/cache/snapshots"]
	if !ok || !snap.ReadOnly || snap.SubPath != "snapshots" {
		t.Errorf("/cache/snapshots mount = %+v, want subPath snapshots read-only", snap)
	}
	checkCacheEnv(t, envMap(c.Env))

	for _, e := range c.Env {
		if e.Name == execAgentAuthEnv && e.Value != "" {
			t.Error("exec token became a literal")
		}
	}
	if p := pod.Spec.SecurityContext.FSGroupChangePolicy; p == nil || *p != corev1.FSGroupChangeOnRootMismatch {
		t.Errorf("fsGroupChangePolicy = %v, want OnRootMismatch", p)
	}
}

func TestKubeSnapshotsWritableOnlyForPrewarm(t *testing.T) {
	k := NewKube(fake.NewClientset(), KubeConfig{Image: "img"})
	task := cacheMounts(k.buildSandboxPod("sandbox-t1", CreateOpts{TaskID: "t1", WorkspaceID: "acme"}).Spec.Containers[0])
	if !task["/cache/snapshots"].ReadOnly {
		t.Error("a task sandbox must see /cache/snapshots read-only")
	}
	prewarm := cacheMounts(k.buildSandboxPod("sandbox-p1", CreateOpts{TaskID: "p1", WorkspaceID: "acme", SnapshotsWritable: true}).Spec.Containers[0])
	m, ok := prewarm["/cache/snapshots"]
	if !ok || m.ReadOnly || m.SubPath != "snapshots" {
		t.Errorf("prewarm /cache/snapshots = %+v, want subPath snapshots read-write", m)
	}
	if prewarm["/cache"].ReadOnly {
		t.Error("/cache must stay read-write")
	}
}

func TestKubeNoCacheWithoutWorkspaceOrWhenOff(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		cfg  KubeConfig
		opts CreateOpts
		ns   string
	}{
		"no workspace": {KubeConfig{Image: "img"}, CreateOpts{TaskID: "t1"}, "octopus-sandboxes"},
		"cache off":    {KubeConfig{Image: "img", CacheSize: "off"}, CreateOpts{TaskID: "t1", WorkspaceID: "acme"}, "tenant-acme"},
	} {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset()
			k := NewKube(client, tc.cfg)
			res, err := k.Create(ctx, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			pod, err := client.CoreV1().Pods(tc.ns).Get(ctx, res.ContainerID, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if m := cacheMounts(pod.Spec.Containers[0]); len(m) != 0 {
				t.Errorf("cache mounted: %+v", m)
			}
			if _, set := envMap(pod.Spec.Containers[0].Env)[CacheDirEnv]; set {
				t.Errorf("%s set without a cache", CacheDirEnv)
			}
			if _, err := client.CoreV1().PersistentVolumeClaims(tc.ns).Get(ctx, kuberuntime.CacheClaimName, metav1.GetOptions{}); err == nil {
				t.Error("a cache claim was created")
			}
		})
	}
}

func TestKubeStartsWithoutCacheWhenClaimFails(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("create", "persistentvolumeclaims", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		pvc := a.(k8stesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim)
		if pvc.Name == kuberuntime.CacheClaimName {
			return true, nil, errors.New("exceeded quota")
		}
		return false, nil, nil
	})
	k := NewKube(client, KubeConfig{Image: "img"})
	ctx := context.Background()
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m := cacheMounts(pod.Spec.Containers[0]); len(m) != 0 {
		t.Errorf("cache mounted although its claim failed: %+v", m)
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name == cacheVolumeName {
			t.Error("pod names the cache claim that failed")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestKubeExecInheritsPodEnv(t *testing.T) {
	k := NewKube(fake.NewClientset(), KubeConfig{Image: "img"})
	k.pods["sandbox-t1"] = sbRec{namespace: "tenant-acme", token: "tok"}
	var got execagent.ExecRequest
	k.httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"stdout":"","stderr":"","exit_code":0}`)),
		}, nil
	})}
	if _, err := k.Exec(context.Background(), "sandbox-t1", ExecOpts{Command: "go build", Env: map[string]string{"CGO_ENABLED": "0"}}); err != nil {
		t.Fatal(err)
	}
	if !got.InheritEnv || got.Env["CGO_ENABLED"] != "0" {
		t.Errorf("exec request = %+v, want the caller's env on top of the pod's (inherit_env)", got)
	}
}

func TestCacheVolumeNames(t *testing.T) {
	for ws, want := range map[string]string{
		"a1b2c3d4e5f60718": "octopus-cache-a1b2c3d4e5f60718",
		"Acme/one two":     "octopus-cache-Acme-one-two",
		"":                 "",
	} {
		cache, snaps := CacheVolumeNames(ws)
		if cache != want {
			t.Errorf("CacheVolumeNames(%q) = %q, want %q", ws, cache, want)
		}
		if want != "" && snaps != want+"-snapshots" {
			t.Errorf("snapshots volume of %q = %q", ws, snaps)
		}
	}
}

func TestDockerCacheMounts(t *testing.T) {
	byTarget := func(ms []mount.Mount) map[string]mount.Mount {
		out := map[string]mount.Mount{}
		for _, m := range ms {
			out[m.Target] = m
		}
		return out
	}
	task := byTarget(dockerCacheMounts("ws1", false))
	root, snap := task["/cache"], task["/cache/snapshots"]
	if root.Type != mount.TypeVolume || root.Source != "octopus-cache-ws1" || root.ReadOnly {
		t.Errorf("/cache = %+v, want the workspace volume read-write", root)
	}
	if snap.Type != mount.TypeVolume || snap.Source != "octopus-cache-ws1-snapshots" || !snap.ReadOnly {
		t.Errorf("/cache/snapshots = %+v, want the snapshots volume read-only", snap)
	}
	for _, m := range []mount.Mount{root, snap} {
		if m.VolumeOptions == nil || m.VolumeOptions.Labels[CacheVolumeLabel] != "ws1" {
			t.Errorf("%s volume must carry %s=ws1 for uninstall: %+v", m.Target, CacheVolumeLabel, m.VolumeOptions)
		}
	}
	if byTarget(dockerCacheMounts("ws1", true))["/cache/snapshots"].ReadOnly {
		t.Error("prewarm must get /cache/snapshots read-write")
	}

	d := &Docker{cache: true}
	ms, env := d.cacheWiring(CreateOpts{TaskID: "t1", WorkspaceID: "ws1"})
	if len(ms) != 2 {
		t.Errorf("cache mounts = %d, want 2", len(ms))
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	checkCacheEnv(t, got)
	if ms, env := d.cacheWiring(CreateOpts{TaskID: "t1"}); ms != nil || env != nil {
		t.Error("no workspace id must mean no cache, not a shared one")
	}
	if ms, env := (&Docker{}).cacheWiring(CreateOpts{TaskID: "t1", WorkspaceID: "ws1"}); ms != nil || env != nil {
		t.Error("cache off must mount nothing")
	}
}

type fakeVolumeAPI struct {
	removed []string
	errs    map[string]error
}

func (f *fakeVolumeAPI) VolumeRemove(_ context.Context, name string, _ bool) error {
	f.removed = append(f.removed, name)
	return f.errs[name]
}

func TestRemoveCacheVolumes(t *testing.T) {
	ctx := context.Background()
	api := &fakeVolumeAPI{errs: map[string]error{
		"octopus-cache-ws1-snapshots": errdefs.NotFound(errors.New("no such volume")),
	}}
	if err := RemoveCacheVolumes(ctx, api, "ws1"); err != nil {
		t.Fatalf("a missing volume is fine: %v", err)
	}
	if strings.Join(api.removed, ",") != "octopus-cache-ws1-snapshots,octopus-cache-ws1" {
		t.Errorf("removed %v", api.removed)
	}

	busy := &fakeVolumeAPI{errs: map[string]error{"octopus-cache-ws1": errdefs.Conflict(errors.New("volume is in use"))}}
	if err := RemoveCacheVolumes(ctx, busy, "ws1"); err == nil {
		t.Error("a volume still in use must fail the teardown, so the purge is retried")
	}

	none := &fakeVolumeAPI{}
	if err := RemoveCacheVolumes(ctx, none, ""); err != nil || len(none.removed) != 0 {
		t.Errorf("no workspace: err=%v removed=%v", err, none.removed)
	}
}
