package sandbox

import (
	"slices"
	"strings"
	"testing"

	"github.com/docker/go-connections/nat"
)

func TestLimitsFromEnv(t *testing.T) {
	def := limitsFromEnv(func(string) string { return "" })
	if def.publishIP != "127.0.0.1" || def.pids != 4096 || def.nanoCPUs != 2e9 {
		t.Errorf("defaults = %+v", def)
	}
	env := map[string]string{
		EnvSandboxPublishIP: "0.0.0.0", EnvSandboxPidsLimit: "512", EnvSandboxCPUs: "1.5",
	}
	got := limitsFromEnv(func(k string) string { return env[k] })
	if got.publishIP != "0.0.0.0" || got.pids != 512 || got.nanoCPUs != 1.5e9 {
		t.Errorf("overrides = %+v", got)
	}
	bad := map[string]string{
		EnvSandboxPublishIP: "evil.example", EnvSandboxPidsLimit: "-1", EnvSandboxCPUs: "lots",
	}
	if got := limitsFromEnv(func(k string) string { return bad[k] }); got != def {
		t.Errorf("malformed values must fall back to the defaults, got %+v", got)
	}
}

func TestSandboxHostConfigIsBounded(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_HOST_GATEWAY", "")
	d := &Docker{limits: limitsFromEnv(func(string) string { return "" })}
	bindings := nat.PortMap{"3000/tcp": {{HostIP: d.limits.publishIP}}}
	hc := d.sandboxHostConfig(nil, bindings, "octopus-agents", false, 1<<30)
	if !slices.Equal(hc.CapDrop, []string{"ALL"}) {
		t.Errorf("CapDrop = %v", hc.CapDrop)
	}
	for _, c := range []string{"NET_RAW", "MKNOD", "SYS_ADMIN", "SYS_CHROOT"} {
		if slices.Contains(hc.CapAdd, c) {
			t.Errorf("sandbox must not keep %s", c)
		}
	}
	if hc.PidsLimit == nil || *hc.PidsLimit != 4096 || hc.NanoCPUs != 2e9 {
		t.Errorf("pids/cpu caps missing: pids=%v cpus=%d", hc.PidsLimit, hc.NanoCPUs)
	}
	if hc.Memory != 1<<30 || hc.MemorySwap != 1<<30 {
		t.Errorf("memory cap = %d/%d", hc.Memory, hc.MemorySwap)
	}
	if string(hc.NetworkMode) != "octopus-agents" {
		t.Errorf("NetworkMode = %q", hc.NetworkMode)
	}
	if hc.PortBindings["3000/tcp"][0].HostIP != "127.0.0.1" {
		t.Error("ports must publish on loopback by default")
	}
	if !slices.Contains(hc.SecurityOpt, "no-new-privileges:true") {
		t.Error("no-new-privileges missing")
	}
	// The default /tmp is a quarter of the 1 GiB cap: a tmpfs is memory.
	if got := hc.Tmpfs["/tmp"]; !strings.HasSuffix(got, ",size=256m") || !strings.Contains(got, "mode=1777") {
		t.Errorf("/tmp must be a sized tmpfs, got %q", got)
	}
	explicit := &Docker{limits: limitsFromEnv(func(k string) string {
		return map[string]string{EnvSandboxTmpSize: "2g"}[k]
	})}
	if got := explicit.sandboxHostConfig(nil, nil, "", false, 1<<30).Tmpfs["/tmp"]; !strings.HasSuffix(got, ",size=2g") {
		t.Errorf("%s=2g is used as given, got %q", EnvSandboxTmpSize, got)
	}
	off := &Docker{limits: limitsFromEnv(func(k string) string {
		return map[string]string{EnvSandboxTmpSize: "off"}[k]
	})}
	if tmp := off.sandboxHostConfig(nil, nil, "", false, 0).Tmpfs; len(tmp) != 0 {
		t.Errorf("%s=off keeps the disk-backed /tmp, got %v", EnvSandboxTmpSize, tmp)
	}

	rootless := d.sandboxHostConfig(nil, nil, "", true, 0)
	if len(rootless.CapAdd) != 0 {
		t.Errorf("rootless sandbox keeps no capabilities, got %v", rootless.CapAdd)
	}
	if hc.Init != nil || rootless.Init != nil {
		t.Error("sandboxHostConfig must not set Init; dockerx.ApplyInit decides it from the image")
	}
}

func TestSandboxContainerKeepsTheImageInit(t *testing.T) {
	cfg := sandboxContainerConfig("octopus-runtime:1", nil, nil, nil, false)
	if len(cfg.Entrypoint) != 0 {
		t.Errorf("Entrypoint = %q, want unset (the image's tini)", cfg.Entrypoint)
	}
	if !slices.Equal(cfg.Cmd, []string{"sleep", "infinity"}) {
		t.Errorf("Cmd = %q", cfg.Cmd)
	}
	if cfg.User != "" {
		t.Errorf("User = %q, want the image's", cfg.User)
	}
	if rootless := sandboxContainerConfig("octopus-runtime:1", nil, nil, nil, true); rootless.User != "1000:1000" {
		t.Errorf("rootless User = %q", rootless.User)
	}
}

func TestSandboxEnvForwardsProxyAndCA(t *testing.T) {
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("HTTPS_PROXY", "http://proxy:3128")
	env := sandboxEnv([]string{"NODE_OPTIONS=--max-old-space-size=1024"}, true)
	for _, want := range []string{
		"HTTPS_PROXY=http://proxy:3128", "https_proxy=http://proxy:3128",
		"NODE_EXTRA_CA_CERTS=/etc/octopus/ca.crt", "NODE_OPTIONS=--max-old-space-size=1024",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env missing %s: %v", want, env)
		}
	}
	if slices.Contains(env, "NODE_OPTIONS=--max-old-space-size=4096") {
		t.Error("caller's NODE_OPTIONS must win over the default")
	}
	if slices.Contains(sandboxEnv(nil, false), "NODE_EXTRA_CA_CERTS=/etc/octopus/ca.crt") {
		t.Error("no CA, no NODE_EXTRA_CA_CERTS")
	}
}

// A 3 GB Colima VM under the 6 GB default cap: the cap would never stop a /tmp of 2 GB from taking
// two thirds of the VM's memory, so the default follows the host too.
func TestDefaultTmpfsFitsTheDockerHost(t *testing.T) {
	cases := []struct {
		mem, host int64
		want      string
	}{
		{6 << 30, 3 << 30, "384m"},
		{6 << 30, 64 << 30, "1536m"},
		{16 << 30, 64 << 30, "2048m"},
		{0, 0, "2048m"},
		{-1, 0, "2048m"},
		{6 << 30, 256 << 20, "64m"},
	}
	for _, c := range cases {
		if got := defaultTmpfsSize(c.mem, c.host); got != c.want {
			t.Errorf("cap %d, host %d: %s, want %s", c.mem, c.host, got, c.want)
		}
	}
	d := &Docker{limits: limitsFromEnv(func(string) string { return "" })}
	d.hostMem.Store(3 << 30)
	if got := d.sandboxHostConfig(nil, nil, "", false, 6<<30).Tmpfs["/tmp"]; !strings.HasSuffix(got, ",size=384m") {
		t.Errorf("default /tmp on a 3 GB host: %q", got)
	}
}
