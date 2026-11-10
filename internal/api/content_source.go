package api

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/builtin"
)

type contentFile struct {
	Path string
	Body []byte
}

// Implementations must return files sorted by Path: the bundle archive must be byte-identical for
// the same content.
type contentSource interface {
	// A missing owner yields no files and no error; the bundle must stay usable without it.
	Files(ctx context.Context, kind, owner string) ([]contentFile, error)
}

type fileContent struct{ root string }

func (f fileContent) Files(_ context.Context, kind, owner string) ([]contentFile, error) {
	if !safeComponent(owner) {
		return nil, nil
	}
	switch kind {
	case agentstore.KindAgent:
		var out []contentFile
		for _, sub := range []string{"prompts", "auditor"} {
			files, err := readTree(filepath.Join(f.root, "agents", owner, sub), sub)
			if err != nil {
				return nil, err
			}
			out = append(out, files...)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
		return out, nil
	case agentstore.KindSkill:
		return readTree(filepath.Join(f.root, "skills", owner), "")
	case agentstore.KindTool:
		var out []contentFile
		for _, meta := range []string{agentstore.ToolManualFile, "tool.yaml"} {
			body, err := os.ReadFile(filepath.Join(f.root, "tools", owner, meta)) //nolint:gosec // root plus a validated component
			if err != nil {
				continue
			}
			out = append(out, contentFile{Path: meta, Body: body})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
		return out, nil
	}
	return nil, nil
}

func readTree(dir, prefix string) ([]contentFile, error) {
	paths, err := regularFilePaths(dir)
	if err != nil {
		return nil, err
	}
	out := make([]contentFile, 0, len(paths))
	for _, p := range paths {
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return nil, apperr.Wrap(relErr, "api.agents.errors.relative_path", "path", p)
		}
		body, readErr := os.ReadFile(p) //nolint:gosec // p came from walking dir
		if readErr != nil {
			return nil, apperr.Wrap(readErr, "api.agents.errors.read_file", "path", p)
		}
		stored := filepath.ToSlash(rel)
		if prefix != "" {
			stored = prefix + "/" + stored
		}
		out = append(out, contentFile{Path: stored, Body: body})
	}
	return out, nil
}

func regularFilePaths(dir string) ([]string, error) {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil, nil
	}
	var paths []string
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "runs" || (p != dir && strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		// Regular files only: following a symlink out of the content root is the escape this
		// endpoint must not offer.
		if info, statErr := d.Info(); statErr == nil && info.Mode().IsRegular() {
			paths = append(paths, p)
		}
		return nil
	})
	if walkErr != nil {
		return nil, apperr.Wrap(walkErr, "api.agents.errors.walk_dir", "path", dir)
	}
	sort.Strings(paths)
	return paths, nil
}

type dbContent struct {
	store  *agentstore.Store
	tenant string
}

func (d dbContent) Files(ctx context.Context, kind, owner string) ([]contentFile, error) {
	files, err := d.store.ReadOwner(ctx, d.tenant, kind, owner)
	if err != nil {
		return nil, err
	}
	out := make([]contentFile, 0, len(files))
	for i := range files {
		out = append(out, contentFile{Path: files[i].Path, Body: files[i].Body})
	}
	return out, nil
}

// The system copy of a skill wins so every workspace reads the same one; handle_skills.go refuses
// workspace skills under a system name, and an older one is shadowed.
type catalogContent struct {
	store  *agentstore.Store
	tenant string
}

func (c catalogContent) Files(ctx context.Context, kind, owner string) ([]contentFile, error) {
	switch kind {
	case agentstore.KindTool:
		return dbContent{store: c.store, tenant: agentstore.GlobalTenant}.Files(ctx, kind, owner)
	case agentstore.KindSkill:
		sys, err := dbContent{store: c.store, tenant: agentstore.SystemTenant}.Files(ctx, kind, owner)
		if err != nil || len(sys) > 0 {
			return sys, err
		}
	}
	return dbContent(c).Files(ctx, kind, owner)
}

// Falls back per server and whole: a server the source knows, even only by tool.yaml, is served
// from the source alone. catalogTools must apply the same order.
type withNativeTools struct{ contentSource }

func (n withNativeTools) Files(ctx context.Context, kind, owner string) ([]contentFile, error) {
	files, err := n.contentSource.Files(ctx, kind, owner)
	if err != nil || kind != agentstore.KindTool || len(files) > 0 {
		return files, err
	}
	return nativeToolFiles(owner), nil
}

func nativeToolFiles(owner string) []contentFile {
	if !safeComponent(owner) {
		return nil
	}
	native := builtin.NativeTools()
	var out []contentFile
	for _, name := range []string{agentstore.ToolManualFile, "tool.yaml"} {
		body, err := fs.ReadFile(native, owner+"/"+name)
		if err != nil {
			continue
		}
		out = append(out, contentFile{Path: name, Body: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
