package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// The chain catches edits, deletions and insertions by anyone who does not recompute it. A
// superuser rewrite or tail truncation needs OCTOPUS_AUDIT_SINK or a recorded head hash.

// Every daemon on the database must use this same key (ASCII "octoaudt").
const auditChainLockKey int64 = 0x6f63746f61756474

// Fields are strconv.Quote'd so field boundaries are unambiguous. Changing this serialization
// breaks verification of every stored row.
func rowHash(prev string, e *Entry, meta string) string {
	fields := []string{
		e.ID, string(e.Kind), e.WorkspaceID, e.ActorID, e.ActorType, e.ActorLabel, e.ActorEmail,
		e.ResourceID, e.ResourceType, e.Summary, meta, e.ClientIP, e.UserAgent,
		e.CreatedAt.UTC().Format(auditTimeFormat),
	}
	var b strings.Builder
	b.WriteString(prev)
	for _, f := range fields {
		b.WriteByte('\n')
		b.WriteString(strconv.Quote(f))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

type ChainReport struct {
	Checked         int       `json:"checked"`
	Legacy          int       `json:"legacy"`
	OK              bool      `json:"ok"`
	BrokenAt        string    `json:"broken_at,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Cause           string    `json:"cause,omitempty"`
	BrokenCreatedAt time.Time `json:"broken_created_at,omitzero"`
	Head            string    `json:"head,omitempty"`
}

func (s *Store) VerifyChain(ctx context.Context) (ChainReport, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, workspace_id, actor_id, actor_type, actor_label, actor_email,
		       resource_id, resource_type, summary, metadata, client_ip, user_agent,
		       created_at, prev_hash, row_hash
		FROM audit_log ORDER BY seq ASC`)
	if err != nil {
		return ChainReport{}, err
	}
	defer func() { _ = rows.Close() }()

	rep := ChainReport{OK: true}
	var prev string
	first := true
	for rows.Next() {
		var e Entry
		var kind, meta string
		var created time.Time
		if err := rows.Scan(&e.ID, &kind, &e.WorkspaceID, &e.ActorID, &e.ActorType, &e.ActorLabel,
			&e.ActorEmail, &e.ResourceID, &e.ResourceType, &e.Summary, &meta, &e.ClientIP,
			&e.UserAgent, &created, &e.PrevHash, &e.RowHash); err != nil {
			return ChainReport{}, err
		}
		e.Kind = EventKind(kind)
		e.CreatedAt = created

		if e.RowHash == "" {
			if !first {
				return rep.broken(ctx, &e, "audit.chain.unhashed_row"), nil
			}
			rep.Legacy++
			continue
		}
		// The first chained row may point at a predecessor the retention sweep deleted; every later
		// row must point at the row before it.
		if !first && e.PrevHash != prev {
			return rep.broken(ctx, &e, "audit.chain.prev_hash_mismatch"), nil
		}
		if got := rowHash(e.PrevHash, &e, meta); got != e.RowHash {
			return rep.broken(ctx, &e, "audit.chain.content_mismatch"), nil
		}
		rep.Checked++
		prev = e.RowHash
		first = false
	}
	if err := rows.Err(); err != nil {
		return ChainReport{}, err
	}
	rep.Head = prev
	return rep, nil
}

func (r ChainReport) broken(ctx context.Context, e *Entry, key string) ChainReport {
	r.OK = false
	r.BrokenAt = e.ID
	r.BrokenCreatedAt = e.CreatedAt.UTC()
	r.Cause = i18n.TCtx(ctx, key)
	r.Reason = i18n.TCtx(ctx, "audit.chain.broken_at", "reason", r.Cause, "id", e.ID)
	return r
}
