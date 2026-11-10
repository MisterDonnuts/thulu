package agentstore

// treeManifest and storeManifest must mirror what GET /run/bundle writes. Comparing manifests
// proves identical archives only while the bundle uses fixed mode, zero mtime and sorted paths.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type VerifyReport struct {
	Checked     int      `json:"checked"`
	Agents      int      `json:"agents"`
	Mismatches  []string `json:"mismatches,omitempty"`
	MissingRows []string `json:"missing_rows,omitempty"`
}

func (r VerifyReport) OK() bool { return len(r.Mismatches) == 0 && len(r.MissingRows) == 0 }

type bundleEntry struct {
	Path string
	SHA  string
}

func (s *Store) VerifyAgainstTree(ctx context.Context, tenant, root string) (VerifyReport, error) {
	var rep VerifyReport

	discovered, err := types.Discover(root)
	if err != nil {
		return rep, apperr.Wrap(err, "agentstore.errors.discover_agents", "root", root)
	}
	for i := range discovered {
		at := discovered[i]
		if nameErr := ValidateName(at.Name); nameErr != nil {
			continue
		}
		stored, getErr := s.GetAgent(ctx, tenant, at.Name)
		if getErr != nil {
			rep.MissingRows = append(rep.MissingRows, at.Name+": "+getErr.Error())
			continue
		}
		rep.Agents++

		// A stage in SkillsByStage or MCPByStage but not in ValidStages still produces a bundle, so
		// it is checked too.
		for _, stage := range stagesToCheck(at) {
			fromFiles, fileErr := treeManifest(root, at, stage)
			if fileErr != nil {
				return rep, fileErr
			}
			fromRows, rowErr := s.storeManifest(ctx, tenant, stored, stage)
			if rowErr != nil {
				return rep, rowErr
			}
			rep.Checked++
			if diff := diffManifests(fromFiles, fromRows); diff != "" {
				rep.Mismatches = append(rep.Mismatches,
					fmt.Sprintf("%s/%s: %s", at.Name, stage, diff))
			}
		}
	}
	sort.Strings(rep.Mismatches)
	return rep, nil
}

func stagesToCheck(at types.AgentType) []string {
	seen := map[string]struct{}{}
	for _, st := range at.ValidStages {
		seen[string(st)] = struct{}{}
	}
	for st := range at.SkillsByStage {
		seen[st] = struct{}{}
	}
	for st := range at.MCPByStage {
		seen[st] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for st := range seen {
		out = append(out, st)
	}
	sort.Strings(out)
	return out
}

func treeManifest(root string, at types.AgentType, stage string) ([]bundleEntry, error) {
	var out []bundleEntry
	add := func(dir, prefix string) error {
		paths, err := regularFiles(dir)
		if err != nil {
			return err
		}
		for _, p := range paths {
			rel, relErr := filepath.Rel(dir, p)
			if relErr != nil {
				return apperr.Wrap(relErr, "agentstore.errors.rel_path", "path", p)
			}
			body, readErr := os.ReadFile(p) //nolint:gosec // path came from walking root
			if readErr != nil {
				return apperr.Wrap(readErr, "agentstore.errors.read_path", "path", p)
			}
			out = append(out, bundleEntry{Path: prefix + filepath.ToSlash(rel), SHA: sha(body)})
		}
		return nil
	}

	for _, sub := range []string{"prompts", "auditor"} {
		if err := add(filepath.Join(root, "agents", at.Name, sub), sub+"/"); err != nil {
			return nil, err
		}
	}
	if err := add(filepath.Join(root, "skills", "_base"), "skills/_base/"); err != nil {
		return nil, err
	}
	for _, name := range at.SkillsByStage[stage] {
		name = strings.TrimSpace(name)
		if err := add(filepath.Join(root, "skills", name), "skills/"+name+"/"); err != nil {
			return nil, err
		}
	}
	for _, entry := range at.MCPByStage[stage] {
		name, _, _ := strings.Cut(strings.TrimSpace(entry), "@")
		body, err := os.ReadFile(filepath.Join(root, "tools", name, ToolManualFile)) //nolint:gosec // root plus a label-derived name
		if err != nil {
			continue
		}
		out = append(out, bundleEntry{Path: "tools/" + name + "/" + ToolManualFile, SHA: sha(body)})
	}
	sortEntries(out)
	return out, nil
}

func (s *Store) storeManifest(ctx context.Context, tenant string, a Agent, stage string) ([]bundleEntry, error) {
	var out []bundleEntry
	add := func(kind, owner, prefix string) error {
		files, err := s.ListOwner(ctx, tenant, kind, owner)
		if err != nil {
			return err
		}
		for i := range files {
			out = append(out, bundleEntry{Path: prefix + files[i].Path, SHA: files[i].SHA})
		}
		return nil
	}

	if err := add(KindAgent, a.Name, ""); err != nil {
		return nil, err
	}
	if err := add(KindSkill, "_base", "skills/_base/"); err != nil {
		return nil, err
	}
	for _, name := range a.SkillsByStage[stage] {
		name = strings.TrimSpace(name)
		if err := add(KindSkill, name, "skills/"+name+"/"); err != nil {
			return nil, err
		}
	}
	for _, entry := range a.MCPByStage[stage] {
		name, _, _ := strings.Cut(strings.TrimSpace(entry), "@")
		// Only the manual goes into the bundle. Manuals are install-wide (GlobalTenant), whatever
		// workspace runs the agent.
		files, err := s.ListOwner(ctx, GlobalTenant, KindTool, name)
		if err != nil {
			return nil, err
		}
		for i := range files {
			if files[i].Path != ToolManualFile {
				continue
			}
			out = append(out, bundleEntry{Path: "tools/" + name + "/" + files[i].Path, SHA: files[i].SHA})
		}
	}
	sortEntries(out)
	return out, nil
}

func sortEntries(e []bundleEntry) {
	sort.Slice(e, func(i, j int) bool { return e[i].Path < e[j].Path })
}

func sha(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func diffManifests(want, got []bundleEntry) string {
	wantBy := map[string]string{}
	for _, e := range want {
		wantBy[e.Path] = e.SHA
	}
	gotBy := map[string]string{}
	for _, e := range got {
		gotBy[e.Path] = e.SHA
	}
	var problems []string
	for p, w := range wantBy {
		g, ok := gotBy[p]
		if !ok {
			problems = append(problems, i18n.Text("agentstore.cli.manifest_missing", "path", p))
			continue
		}
		if g != w {
			problems = append(problems, i18n.Text("agentstore.cli.manifest_differs", "path", p))
		}
	}
	for p := range gotBy {
		if _, ok := wantBy[p]; !ok {
			problems = append(problems, i18n.Text("agentstore.cli.manifest_unexpected", "path", p))
		}
	}
	sort.Strings(problems)
	return strings.Join(problems, "; ")
}
