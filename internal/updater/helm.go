package updater

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

const chartLayerType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"

const maxChart = 16 << 20

func (u *updateRun) chartRef(m *release.Manifest) (string, error) {
	if m.Chart.Ref == "" || m.Chart.Digest == "" {
		return "", apperr.NotFound("updater.errors.no_chart", "version", m.Version)
	}
	repo := repository(strings.TrimPrefix(m.Chart.Ref, "oci://"))
	if u.opts.ChartRegistry != "" || u.opts.Registry != "" {
		repo = mirrorChartRepo(u.opts.ChartRegistry, u.opts.Registry, repo)
	}
	return repo + "@" + m.Chart.Digest, nil
}

func fetchChart(ctx context.Context, ref string) ([]byte, error) {
	d, err := name.NewDigest(ref)
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.chart", "ref", ref)
	}
	img, err := remote.Image(d, release.RemoteOptions(ctx)...)
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.chart", "ref", ref)
	}
	mf, err := img.Manifest()
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.chart", "ref", ref)
	}
	for i := range mf.Layers {
		l := &mf.Layers[i]
		if string(l.MediaType) != chartLayerType {
			continue
		}
		if l.Size > maxChart {
			return nil, apperr.TooLarge("updater.errors.chart_size", "ref", ref, "size", l.Size)
		}
		layer, err := img.LayerByDigest(l.Digest)
		if err != nil {
			return nil, apperr.Wrap(err, "updater.errors.chart", "ref", ref)
		}
		rc, err := layer.Compressed()
		if err != nil {
			return nil, apperr.Wrap(err, "updater.errors.chart", "ref", ref)
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxChart+1))
		_ = rc.Close()
		if err != nil {
			return nil, apperr.Wrap(err, "updater.errors.chart", "ref", ref)
		}
		return b, nil
	}
	return nil, apperr.NotFound("updater.errors.chart_no_layer", "ref", ref, "type", chartLayerType)
}

// Must match the chart's values keys for native MCP servers (thulu-github → thuluGithub).
func mcpValueKey(server string) string {
	parts := strings.Split(server, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func (u *updateRun) helmArgs(m *release.Manifest, chartPath string) ([]string, error) {
	daemon, err := MirrorRef(u.opts.Registry, m.Images["octopus"])
	if err != nil {
		return nil, err
	}
	repo, digest, _ := strings.Cut(daemon, "@")
	tag := initwizard.ImageTag(m.Version)
	args := []string{
		"upgrade", "--install", u.in.Release, chartPath,
		"--namespace", u.in.Namespace,
		"--wait", "--atomic", "--timeout", "10m",
		"--set", "image.repository=" + repo,
		"--set", "image.tag=" + tag,
		"--set", "image.digest=" + digest,
		"--set", "mcp.imageTag=" + tag,
	}
	images := [][2]string{{"agentRuntime.sandboxImage", m.Images["octopus-sandbox"]}}
	for _, e := range agentimage.Engines {
		images = append(images, [2]string{"agentRuntime.engineImages." + e, m.Images[agentimage.Name(e)]})
	}
	// Releases with one agent image set agentRuntime.agentImage on every upgrade, so --reuse-values
	// would carry that pin over; clear it. A values file never held the pin.
	if u.opts.ReuseValues {
		args = append(args, "--set", "agentRuntime.agentImage=")
	}
	for _, kv := range images {
		key, img := kv[0], kv[1]
		if img == "" {
			continue
		}
		ref, err := MirrorRef(u.opts.Registry, img)
		if err != nil {
			return nil, err
		}
		args = append(args, "--set", key+"="+ref)
	}
	for _, server := range dockergc.NativeMCPServers {
		img := m.Images[dockergc.MCPImage(server)]
		if img == "" {
			continue
		}
		ref, err := MirrorRef(u.opts.Registry, img)
		if err != nil {
			return nil, err
		}
		r, d, _ := strings.Cut(ref, "@")
		k := "mcp." + mcpValueKey(server) + ".image."
		// The chart renders <repository>:<tag>, so tag "<version>@<digest>" pins the digest without
		// a new values key.
		args = append(args, "--set", k+"repository="+r, "--set", k+"tag="+tag+"@"+d)
	}
	values, valuesErr := u.helmValues()
	if valuesErr != nil {
		return nil, valuesErr
	}
	return append(args, values...), nil
}

// Without -values, deploy/values-<release>.yaml or --reuse-values, helm renders chart defaults and
// drops SSO,
// the DSN and the RuntimeClass, so refuse.
func (u *updateRun) helmValues() ([]string, error) {
	var out []string
	defaultFile := filepath.Join("deploy", fmt.Sprintf("values-%s.yaml", u.in.Release))
	switch {
	case u.opts.Values != "":
		path := u.opts.Values
		if !filepath.IsAbs(path) {
			abs, err := filepath.Abs(path)
			if err != nil {
				return nil, apperr.Wrap(err, "updater.errors.helm_values_file", "path", path)
			}
			path = abs
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return nil, apperr.NotFound("updater.errors.helm_values_file", "path", path)
		}
		out = append(out, "-f", path)
	default:
		if _, err := os.Stat(filepath.Join(u.in.Root, defaultFile)); err == nil {
			out = append(out, "-f", defaultFile)
		}
	}
	if u.opts.ReuseValues {
		out = append(out, "--reuse-values")
	}
	if len(out) == 0 {
		return nil, apperr.Precondition("updater.errors.helm_values_missing", "path", defaultFile)
	}
	return out, nil
}

func kubeImages(m *release.Manifest, registry string) (map[string]string, error) {
	out := map[string]string{}
	names := make([]string, 0, 2+len(agentimage.Engines)+len(dockergc.NativeMCPServers))
	names = append(names, "octopus", "octopus-sandbox")
	for _, e := range agentimage.Engines {
		names = append(names, agentimage.Name(e))
	}
	for _, s := range dockergc.NativeMCPServers {
		names = append(names, dockergc.MCPImage(s))
	}
	for _, n := range names {
		if m.Images[n] == "" {
			continue
		}
		ref, err := MirrorRef(registry, m.Images[n])
		if err != nil {
			return nil, err
		}
		out[n] = ref
	}
	if out["octopus"] == "" {
		return nil, apperr.Invalid("updater.errors.no_daemon_image")
	}
	return out, nil
}

func (u *updateRun) upgradeHelm(ctx context.Context, m *release.Manifest) (ran bool, err error) {
	ref, err := u.chartRef(m)
	if err != nil {
		return false, err
	}
	if err = u.deps.imageVerifier(m)(ctx, ref); err != nil {
		return false, apperr.Wrap(err, "updater.errors.chart_signature")
	}
	fetch := u.deps.FetchChart
	if fetch == nil {
		fetch = fetchChart
	}
	tgz, err := fetch(ctx, ref)
	if err != nil {
		return false, err
	}
	dir, err := os.MkdirTemp("", "octopus-chart-")
	if err != nil {
		return false, err
	}
	defer func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			fmt.Fprintln(os.Stderr, i18n.Text("updater.cli.warn_remove_failed", "path", dir, "err", apperr.Text(rerr)))
		}
	}()
	path := filepath.Join(dir, "octopus-"+m.Version+".tgz")
	if serr := os.WriteFile(path, tgz, 0o600); serr != nil {
		return false, serr
	}
	args, err := u.helmArgs(m, path)
	if err != nil {
		return false, err
	}
	u.say("updater.cli.helm_upgrade", "release", u.in.Release, "chart", ref)
	return true, u.opts.Runner.Run(ctx, "helm", args...)
}
