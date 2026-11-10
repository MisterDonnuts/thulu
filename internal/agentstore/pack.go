package agentstore

import (
	"context"
	"encoding/base64"
	"sort"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const PackVersion = 1

type Pack struct {
	Version   int          `json:"version"`
	Revision  int          `json:"revision,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	Agents    []Agent      `json:"agents"`
	Content   []PackedFile `json:"content"`
}

type PackedFile struct {
	Kind  string `json:"kind"`
	Owner string `json:"owner"`
	Path  string `json:"path"`
	Body  string `json:"body"`
}

func (s *Store) ExportPack(ctx context.Context, tenant string) (Pack, error) {
	agents, err := s.ListAgents(ctx, tenant)
	if err != nil {
		return Pack{}, err
	}
	pack := Pack{Version: PackVersion, CreatedAt: time.Now().UTC(), Agents: agents}

	for _, kind := range []string{KindAgent, KindSkill, KindTool} {
		owners, ownerErr := s.ListOwners(ctx, tenant, kind)
		if ownerErr != nil {
			return Pack{}, ownerErr
		}
		for _, owner := range owners {
			files, readErr := s.ReadOwner(ctx, tenant, kind, owner)
			if readErr != nil {
				return Pack{}, readErr
			}
			for i := range files {
				pack.Content = append(pack.Content, PackedFile{
					Kind: kind, Owner: owner, Path: files[i].Path,
					Body: base64.StdEncoding.EncodeToString(files[i].Body),
				})
			}
		}
	}
	sort.Slice(pack.Content, func(i, j int) bool {
		a, b := pack.Content[i], pack.Content[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.Path < b.Path
	})
	return pack, nil
}

func (s *Store) ImportPack(ctx context.Context, tenant string, p Pack, actor string) (ImportStats, error) {
	var st ImportStats
	if p.Version != PackVersion {
		return st, apperr.Invalid("agentstore.errors.pack_version_unsupported", "version", p.Version, "want", PackVersion)
	}
	for i := range p.Agents {
		a := p.Agents[i]
		a.Dir = ""
		if _, err := s.PutAgent(ctx, tenant, a, actor); err != nil {
			return st, err
		}
		st.Agents++
	}
	seenSkills := map[string]struct{}{}
	seenTools := map[string]struct{}{}
	for _, f := range p.Content {
		body, err := base64.StdEncoding.DecodeString(f.Body)
		if err != nil {
			return st, apperr.Wrap(err, "agentstore.errors.decode_pack_file", "kind", f.Kind, "owner", f.Owner, "path", f.Path)
		}
		if _, err := s.PutFile(ctx, tenant, f.Kind, f.Owner, f.Path, body, actor); err != nil {
			return st, apperr.Wrap(err, "agentstore.errors.import_file", "kind", f.Kind, "owner", f.Owner, "path", f.Path)
		}
		st.Files++
		st.Bytes += int64(len(body))
		switch f.Kind {
		case KindSkill:
			seenSkills[f.Owner] = struct{}{}
		case KindTool:
			seenTools[f.Owner] = struct{}{}
		}
	}
	st.Skills = len(seenSkills)
	st.Tools = len(seenTools)
	return st, nil
}
