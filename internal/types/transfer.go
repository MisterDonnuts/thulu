package types

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

const dockerSocketPath = "/var/run/docker.sock"

const runsTarget = "/workspace/runs"

// Must match the agent's uid in every image (the K8s image declares it numerically for PSA). Tar
// entries default to uid 0, and the entrypoint's first write to /workspace/runs then fails with
// EACCES.
const agentUID = 1000

type copyIn struct {
	src    string
	target string
}

func isSocketPassthrough(target string) bool {
	return target == dockerSocketPath
}

func buildSocketMounts(spec *ComposeServiceSpec) []mount.Mount {
	var mounts []mount.Mount
	for _, v := range spec.Volumes {
		src, tgt, ro := ParseVolume(v, "")
		if isSocketPassthrough(tgt) {
			mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: src, Target: tgt, ReadOnly: ro})
		}
	}
	return mounts
}

func buildCopyIns(spec *ComposeServiceSpec, agentType AgentType) []copyIn {
	var ins []copyIn
	for _, v := range spec.Volumes {
		src, tgt, _ := ParseVolume(v, agentType.Dir)
		if isSocketPassthrough(tgt) || tgt == runsTarget {
			continue
		}
		ins = append(ins, copyIn{src: src, target: tgt})
	}
	return ins
}

// Worktrees are live-shared, not copied: the agent commits/pushes mid-run through the daemon, which
// must see its edits.
// In copy mode WorkspacePaths/StagePaths are subpaths of the data volume root, set by the
// dispatcher.
func buildVolumeMounts(params LaunchParams) []mount.Mount {
	if params.DataVolume == "" {
		return nil
	}
	mounts := make([]mount.Mount, 0, len(params.WorkspacePaths)+len(params.StagePaths))
	for name, sub := range params.WorkspacePaths {
		if sub == "" {
			continue
		}
		mounts = append(mounts, mount.Mount{
			Type:          mount.TypeVolume,
			Source:        params.DataVolume,
			Target:        "/workspace/projects/" + name,
			ReadOnly:      params.WorkspaceReadOnly,
			VolumeOptions: &mount.VolumeOptions{Subpath: sub},
		})
	}
	for name, sub := range params.StagePaths {
		if sub == "" {
			continue
		}
		mounts = append(mounts, mount.Mount{
			Type:          mount.TypeVolume,
			Source:        params.DataVolume,
			Target:        "/workspace/projects/" + name + "/.thulu",
			ReadOnly:      true,
			VolumeOptions: &mount.VolumeOptions{Subpath: sub},
		})
	}
	for _, em := range params.ExtraMounts {
		if em.Source == "" || em.Target == "" {
			continue
		}
		mounts = append(mounts, em.DockerMount(params.DataVolume))
	}
	return mounts
}

func applyCopyIns(ctx context.Context, docker *client.Client, containerID string, ins []copyIn) error {
	for _, ci := range ins {
		info, err := os.Stat(ci.src)
		if err != nil {
			log.Printf("[launch] %s", i18n.Log("types.log.copy_in_missing_source", "path", ci.src))
			continue
		}
		if err := copyInto(ctx, docker, containerID, ci, info); err != nil {
			return apperr.Wrap(err, "types.errors.copy_in", "target", ci.target)
		}
	}
	return nil
}

func copyInto(ctx context.Context, docker *client.Client, containerID string, ci copyIn, info os.FileInfo) error {
	dst := filepath.Dir(ci.target)
	if err := mkdirInContainer(ctx, docker, containerID, dst); err != nil {
		return err
	}
	buf, err := tarPath(ci.src, info, ci.target)
	if err != nil {
		return err
	}
	return docker.CopyToContainer(ctx, containerID, dst, buf, container.CopyToContainerOptions{})
}

func mkdirInContainer(ctx context.Context, docker *client.Client, containerID, dir string) error {
	dir = strings.Trim(filepath.ToSlash(dir), "/")
	if dir == "" {
		return nil
	}
	buf, err := dirTar(dir)
	if err != nil {
		return err
	}
	return docker.CopyToContainer(ctx, containerID, "/", buf, container.CopyToContainerOptions{})
}

func dirTar(dir string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755,
		Uid: agentUID, Gid: agentUID,
	}); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

func tarPath(src string, info os.FileInfo, target string) (_ *bytes.Buffer, retErr error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	defer func() {
		if cerr := tw.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()
	base := filepath.Base(target)

	if !info.IsDir() {
		if err := writeTarEntry(tw, src, info, base); err != nil {
			return nil, err
		}
		return buf, nil
	}
	err := filepath.Walk(src, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(src, p)
		if relErr != nil {
			return relErr
		}
		name := base
		if rel != "." {
			name = base + "/" + filepath.ToSlash(rel)
		}
		return writeTarEntry(tw, p, fi, name)
	})
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func writeTarEntry(tw *tar.Writer, path string, fi os.FileInfo, name string) error {
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if werr := tw.WriteHeader(hdr); werr != nil {
		return werr
	}
	if fi.IsDir() {
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is walked from a server-configured agent dir, not request input
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

// Bounds a hostile agent's decompression bomb; run artifacts are well under 256 MB.
const maxCopyOutFile = 256 << 20

func RetrieveOutputs(ctx context.Context, docker *client.Client, containerID, containerPath, dst string) error {
	return copyOutOf(ctx, docker, containerID, containerPath, dst)
}

func copyOutOf(ctx context.Context, docker *client.Client, containerID, containerPath, dst string) error {
	rc, _, err := docker.CopyFromContainer(ctx, containerID, containerPath)
	if err != nil {
		return err
	}
	defer rc.Close()
	root := filepath.Base(strings.TrimRight(containerPath, "/"))
	return untarStripRoot(rc, dst, root)
}

func untarStripRoot(r io.Reader, dst, root string) error {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(hdr.Name, root), "/")
		if rel == "" {
			continue
		}
		if err := extractTarEntry(tr, hdr, dst, rel); err != nil {
			return err
		}
	}
}

// The agent that wrote this tar is untrusted: extraction must go through safepath so "../" entries
// cannot escape dst.
func extractTarEntry(tr *tar.Reader, hdr *tar.Header, dst, rel string) error {
	switch hdr.Typeflag {
	case tar.TypeDir:
		return safepath.MkdirAll(dst, rel, 0o750)
	case tar.TypeReg:
		if d := filepath.Dir(rel); d != "." {
			if err := safepath.MkdirAll(dst, d, 0o750); err != nil {
				return err
			}
		}
		f, err := safepath.Create(dst, rel)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, io.LimitReader(tr, maxCopyOutFile))
		return err
	}
	return nil
}
