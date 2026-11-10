package sandbox

import (
	"context"
	"log"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/errdefs"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/sandbox/sandboxid"
)

// The prewarm job and the images' /etc/profile.d/octopus-cache.sh rely on these paths and env; that
// hook creates the subdirectories (uid 1000)
// and re-prepends the mise shims to PATH, because Debian's /etc/profile resets PATH in a login
// shell.

const (
	CacheMountPath     = "/cache"
	CacheSnapshotsPath = CacheMountPath + "/snapshots"
	CacheDirEnv        = "OCTOPUS_CACHE_DIR"
	// dockergc plans volumes with this label only on uninstall.
	CacheVolumeLabel = sandboxid.CacheVolumeLabel

	cacheSnapshotsSubPath = "snapshots"
	cacheVolumeName       = "cache"
)

// The cache env replaces PATH, so this must repeat the sandbox images' base PATH.
const sandboxBasePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// MISE_TRUSTED_CONFIG_PATHS: the sandbox runs project code anyway, and an untrusted mise.toml would
// block every command on a prompt nobody can answer.
func cacheEnv() [][2]string {
	return [][2]string{
		{CacheDirEnv, CacheMountPath},
		{"npm_config_cache", CacheMountPath + "/npm"},
		{"GOMODCACHE", CacheMountPath + "/go/mod"},
		{"GOCACHE", CacheMountPath + "/go/build"},
		{"PIP_CACHE_DIR", CacheMountPath + "/pip"},
		{"MISE_DATA_DIR", CacheMountPath + "/mise"},
		{"MISE_CACHE_DIR", CacheMountPath + "/mise/cache"},
		{"MISE_YES", "1"},
		{"MISE_TRUSTED_CONFIG_PATHS", "/workspace/projects"},
		{"PATH", CacheMountPath + "/mise/shims:" + sandboxBasePath},
	}
}

func cacheEnvList() []string {
	pairs := cacheEnv()
	out := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

func cacheEnvVars() []corev1.EnvVar {
	pairs := cacheEnv()
	out := make([]corev1.EnvVar, 0, len(pairs))
	for _, kv := range pairs {
		out = append(out, corev1.EnvVar{Name: kv[0], Value: kv[1]})
	}
	return out
}

// Logs and falls back instead of failing: the Kubernetes config already refused a bad value at
// startup, and Docker mode ignores the size.
func cacheSetting(v string) (resource.Quantity, bool) {
	size, on, err := kubeapi.ParseCacheSize(v)
	if err != nil {
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.invalid_cache_size",
			"env", kubeapi.EnvCacheSize, "value", v, "default", kubeapi.DefaultCacheSize, "err", err))
		return resource.MustParse(kubeapi.DefaultCacheSize), true
	}
	return size, on
}

func cacheOnFromEnv(getenv func(string) string) bool {
	_, on := cacheSetting(getenv(kubeapi.EnvCacheSize))
	return on
}

func kubeCacheVolume() corev1.Volume {
	return corev1.Volume{
		Name: cacheVolumeName,
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: kubeapi.CacheClaimName,
		}},
	}
}

func kubeCacheMounts(snapshotsWritable bool) []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: cacheVolumeName, MountPath: CacheMountPath},
		{Name: cacheVolumeName, MountPath: CacheSnapshotsPath, SubPath: cacheSnapshotsSubPath, ReadOnly: !snapshotsWritable},
	}
}

func CacheVolumeNames(workspaceID string) (cache, snapshots string) {
	if workspaceID == "" {
		return "", ""
	}
	cache = "octopus-cache-" + sanitizeTaskID(workspaceID)
	return cache, cache + "-snapshots"
}

// Docker seeds a fresh volume from the image's /cache dirs (owned by uid 1000), which is what makes
// it writable for a rootless sandbox.
func dockerCacheMounts(workspaceID string, snapshotsWritable bool) []mount.Mount {
	cache, snapshots := CacheVolumeNames(workspaceID)
	if cache == "" {
		return nil
	}
	labels := map[string]string{CacheVolumeLabel: workspaceID}
	return []mount.Mount{
		{
			Type: mount.TypeVolume, Source: cache, Target: CacheMountPath,
			VolumeOptions: &mount.VolumeOptions{Labels: labels},
		},
		{
			Type: mount.TypeVolume, Source: snapshots, Target: CacheSnapshotsPath, ReadOnly: !snapshotsWritable,
			VolumeOptions: &mount.VolumeOptions{Labels: labels},
		},
	}
}

type CacheVolumeAPI interface {
	VolumeRemove(ctx context.Context, volumeID string, force bool) error
}

// No force: a volume still in use must fail so the purge is retried after the janitor reaps the
// container.
func RemoveCacheVolumes(ctx context.Context, api CacheVolumeAPI, workspaceID string) error {
	cache, snapshots := CacheVolumeNames(workspaceID)
	if cache == "" {
		return nil
	}
	// The snapshots volume first: it is mounted inside the cache one.
	for _, name := range []string{snapshots, cache} {
		err := api.VolumeRemove(ctx, name, false)
		if err != nil && !errdefs.IsNotFound(err) {
			return apperr.Wrap(err, "sandbox.errors.remove_cache_volume", "name", name)
		}
	}
	return nil
}
