package usage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/engine"

	// DefaultPricing seeds once from the engines registered at that moment; without these imports
	// codex and opencode prices never land.
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
)

type Pricing struct {
	Model              string    `json:"model"`
	InPerMTok          float64   `json:"in_per_mtok"`
	OutPerMTok         float64   `json:"out_per_mtok"`
	CacheReadPerMTok   float64   `json:"cache_read_per_mtok"`
	CacheCreatePerMTok float64   `json:"cache_creation_per_mtok"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func DefaultPricing() []Pricing {
	prices := engine.Prices()
	out := make([]Pricing, 0, len(prices))
	for _, p := range prices {
		out = append(out, fromEnginePrice(p))
	}
	return out
}

func fromEnginePrice(p engine.ModelPrice) Pricing {
	return Pricing{
		Model:              p.Model,
		InPerMTok:          p.InPerMTok,
		OutPerMTok:         p.OutPerMTok,
		CacheReadPerMTok:   p.CacheReadPerMTok,
		CacheCreatePerMTok: p.CacheCreatePerMTok,
	}
}

func seedPricing(ctx context.Context, db *sql.DB) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_pricing`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, dbpkg.Rebind(`INSERT INTO model_pricing
		(model, in_per_mtok, out_per_mtok, cache_read_per_mtok, cache_creation_per_mtok)
		VALUES (?, ?, ?, ?, ?)`))
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, p := range DefaultPricing() {
		if _, err := stmt.ExecContext(ctx, p.Model, p.InPerMTok, p.OutPerMTok,
			p.CacheReadPerMTok, p.CacheCreatePerMTok); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AllPricing(ctx context.Context) ([]Pricing, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT model, in_per_mtok, out_per_mtok,
		cache_read_per_mtok, cache_creation_per_mtok, updated_at
		FROM model_pricing ORDER BY model ASC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pricing
	for rows.Next() {
		var p Pricing
		if err := rows.Scan(&p.Model, &p.InPerMTok, &p.OutPerMTok,
			&p.CacheReadPerMTok, &p.CacheCreatePerMTok, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PricingForModel(ctx context.Context, model string) (Pricing, bool) {
	if model == "" {
		return Pricing{}, false
	}
	if p, ok := s.pricingRow(ctx, model); ok {
		return p, true
	}
	lower := strings.ToLower(model)
	for _, fam := range engine.Prices() {
		if !fam.Family || !strings.Contains(lower, strings.ToLower(fam.Model)) {
			continue
		}
		if p, ok := s.pricingRow(ctx, fam.Model); ok {
			return p, true
		}
	}
	if p, ok := engine.PriceFor(model); ok {
		return fromEnginePrice(p), true
	}
	return Pricing{}, false
}

func (s *Store) pricingRow(ctx context.Context, model string) (Pricing, bool) {
	var p Pricing
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT model, in_per_mtok, out_per_mtok,
		cache_read_per_mtok, cache_creation_per_mtok, updated_at
		FROM model_pricing WHERE model = ?`), model,
	).Scan(&p.Model, &p.InPerMTok, &p.OutPerMTok,
		&p.CacheReadPerMTok, &p.CacheCreatePerMTok, &p.UpdatedAt)
	return p, err == nil
}
