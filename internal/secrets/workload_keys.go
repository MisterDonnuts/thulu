package secrets

import (
	"context"
	"database/sql"
	"encoding/base64"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
	"github.com/MisterDonnuts/thulu/internal/workloadid"
)

func (s *Store) WorkloadIdentityKeys(ctx context.Context) ([]workloadid.Key, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kid, private_key_encrypted, created_at, activates_at, retired_at FROM workload_identity_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []workloadid.Key
	for rows.Next() {
		var (
			k       workloadid.Key
			sealed  string
			retired sql.NullTime
		)
		if err := rows.Scan(&k.KID, &sealed, &k.CreatedAt, &k.ActivatesAt, &retired); err != nil {
			return nil, err
		}
		plain, err := secretbox.Decrypt(secretbox.LabelWorkloadIdentityKey, sealed)
		if err != nil {
			return nil, apperr.Wrap(err, "secrets.errors.decrypt_workload_key", "kid", k.KID)
		}
		if k.Private, err = base64.StdEncoding.DecodeString(plain); err != nil {
			return nil, apperr.Wrap(err, "secrets.errors.decrypt_workload_key", "kid", k.KID)
		}
		if retired.Valid {
			k.RetiredAt = retired.Time
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) AddWorkloadIdentityKey(ctx context.Context, k workloadid.Key, retireAt time.Time) error {
	sealed, err := secretbox.Encrypt(secretbox.LabelWorkloadIdentityKey, base64.StdEncoding.EncodeToString(k.Private))
	if err != nil {
		return apperr.Wrap(err, "secrets.errors.encrypt_workload_key")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, s.rb(
		`UPDATE workload_identity_keys SET retired_at = ? WHERE retired_at IS NULL`), retireAt.UTC()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.rb(
		`INSERT INTO workload_identity_keys (kid, private_key_encrypted, created_at, activates_at)
		 VALUES (?, ?, ?, ?)`), k.KID, sealed, k.CreatedAt.UTC(), k.ActivatesAt.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}
