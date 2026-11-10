package agentstore

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type ImportStats struct {
	Agents  int      `json:"agents"`
	Skills  int      `json:"skills"`
	Tools   int      `json:"tools"`
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Skipped []string `json:"skipped,omitempty"`
}

func (st *ImportStats) add(files int, bytes int64) {
	st.Files += files
	st.Bytes += bytes
}

func (st *ImportStats) skip(what string, why error) {
	st.Skipped = append(st.Skipped, fmt.Sprintf("%s (%s)", what, apperr.Text(why)))
}

func (s *Store) ImportTree(ctx context.Context, tenant, root, actor string, asTemplates bool) (ImportStats, error) {
	var st ImportStats
	if err := s.importAgents(ctx, tenant, root, actor, asTemplates, &st); err != nil {
		return st, err
	}
	if err := s.importSkills(ctx, tenant, root, actor, &st); err != nil {
		return st, err
	}
	if err := s.importTools(ctx, tenant, root, actor, &st); err != nil {
		return st, err
	}
	return st, nil
}

func (s *Store) ImportCatalogTree(ctx context.Context, ws, root, actor string, asTemplates bool) (ImportStats, error) {
	var st ImportStats
	if ws == "" || IsReservedTenant(ws) {
		return st, apperr.Invalid("agentstore.errors.import_needs_workspace")
	}
	if err := s.EnsureSeeded(ctx, ws); err != nil {
		return st, err
	}
	if err := s.importAgents(ctx, ws, root, actor, asTemplates, &st); err != nil {
		return st, err
	}
	if err := s.importSkills(ctx, ws, root, actor, &st); err != nil {
		return st, err
	}
	if err := s.importTools(ctx, GlobalTenant, root, actor, &st); err != nil {
		return st, err
	}
	return st, nil
}

func (s *Store) importAgents(ctx context.Context, tenant, root, actor string, asTemplates bool, st *ImportStats) error {
	dirs, err := subdirs(filepath.Join(root, "agents"))
	if err != nil {
		return err
	}
	discovered, err := types.Discover(root)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.discover_agents", "root", root)
	}
	byName := make(map[string]types.AgentType, len(discovered))
	for i := range discovered {
		byName[discovered[i].Name] = discovered[i]
	}

	for _, name := range dirs {
		if nameErr := ValidateName(name); nameErr != nil {
			st.skip("agents/"+name, nameErr)
			continue
		}
		at, ok := byName[name]
		if !ok {
			st.skip("agents/"+name, apperr.NotFound("agentstore.errors.no_agent_service"))
			continue
		}
		// Dir points into the import tree, which is deleted afterwards; never store it.
		at.Dir = ""
		if _, putErr := s.PutAgent(ctx, tenant, Agent{AgentType: at, IsTemplate: asTemplates}, actor); putErr != nil {
			return putErr
		}
		st.Agents++

		for _, sub := range []string{"prompts", "auditor"} {
			n, b, dirErr := s.importDir(ctx, tenant, KindAgent, name,
				filepath.Join(root, "agents", name, sub), sub, actor)
			if dirErr != nil {
				return dirErr
			}
			st.add(n, b)
		}
	}
	return nil
}

func (s *Store) importSkills(ctx context.Context, tenant, root, actor string, st *ImportStats) error {
	dirs, err := subdirs(filepath.Join(root, "skills"))
	if err != nil {
		return err
	}
	for _, name := range dirs {
		if nameErr := ValidateName(name); nameErr != nil {
			st.skip("skills/"+name, nameErr)
			continue
		}
		n, b, dirErr := s.importDir(ctx, tenant, KindSkill, name,
			filepath.Join(root, "skills", name), "", actor)
		if dirErr != nil {
			return dirErr
		}
		if n > 0 {
			st.Skills++
		}
		st.add(n, b)
	}
	return nil
}

var toolMetaFiles = []string{"skill.md", "tool.yaml"}

func (s *Store) importTools(ctx context.Context, tenant, root, actor string, st *ImportStats) error {
	dirs, err := subdirs(filepath.Join(root, "tools"))
	if err != nil {
		return err
	}
	for _, name := range dirs {
		if nameErr := ValidateName(name); nameErr != nil {
			st.skip("tools/"+name, nameErr)
			continue
		}
		var found bool
		for _, meta := range toolMetaFiles {
			src := filepath.Join(root, "tools", name, meta)
			body, readErr := os.ReadFile(src) //nolint:gosec // import root plus a validated name and a fixed file name
			if readErr != nil {
				continue
			}
			if _, putErr := s.PutFile(ctx, tenant, KindTool, name, meta, body, actor); putErr != nil {
				return apperr.Wrap(putErr, "agentstore.errors.import_tool_meta", "name", name, "meta", meta)
			}
			found = true
			st.add(1, int64(len(body)))
		}
		if found {
			st.Tools++
		}
	}
	return nil
}

func (s *Store) importDir(ctx context.Context, tenant, kind, owner, dir, prefix, actor string) (int, int64, error) {
	files, err := regularFiles(dir)
	if err != nil {
		return 0, 0, err
	}
	var (
		n     int
		bytes int64
	)
	for _, p := range files {
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return n, bytes, apperr.Wrap(relErr, "agentstore.errors.rel_path", "path", p)
		}
		stored := filepath.ToSlash(rel)
		if prefix != "" {
			stored = prefix + "/" + stored
		}
		body, readErr := os.ReadFile(p) //nolint:gosec // path came from walking the import root
		if readErr != nil {
			return n, bytes, apperr.Wrap(readErr, "agentstore.errors.read_path", "path", p)
		}
		if _, putErr := s.PutFile(ctx, tenant, kind, owner, stored, body, actor); putErr != nil {
			return n, bytes, apperr.Wrap(putErr, "agentstore.errors.import_file", "kind", kind, "owner", owner, "path", stored)
		}
		n++
		bytes += int64(len(body))
	}
	return n, bytes, nil
}

func regularFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	var files []string
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// runs/ holds run artifacts (session logs, generated prompts) that grew to hundreds of
			// MB; never import it.
			if d.Name() == "runs" || (p != dir && strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(d.Name(), ".") {
			files = append(files, p)
		}
		return nil
	})
	if walkErr != nil {
		return nil, apperr.Wrap(walkErr, "agentstore.errors.walk_dir", "dir", dir)
	}
	sort.Strings(files)
	return files, nil
}

func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.read_path", "path", dir)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
