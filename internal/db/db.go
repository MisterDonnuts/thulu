package db

import (
	"context"
	"database/sql"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // register the "pgx" driver

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Set on the connection so every query is bounded (values in ms). lockTimeout exists because
// task.Store.Update takes a row lock,
// and a stalled holder would otherwise hang every writer of that row with no error.
const (
	statementTimeout = "30000"
	lockTimeout      = "10000"
)

func Open(dsn string) (*sql.DB, error) {
	l := strings.ToLower(strings.TrimSpace(dsn))
	if !strings.HasPrefix(l, "postgres://") && !strings.HasPrefix(l, "postgresql://") {
		return nil, apperr.Invalid("db.errors.unsupported_dsn", "dsn", dsn)
	}
	bounded, err := withQueryBounds(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := sql.Open("pgx", bounded)
	if err != nil {
		return nil, apperr.Wrap(err, "db.errors.open")
	}
	pool.SetMaxOpenConns(25)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxLifetime(time.Hour)
	return pool, nil
}

func withQueryBounds(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", apperr.WrapKind(apperr.KindInvalid, err, "db.errors.parse_dsn")
	}
	q := u.Query()
	for key, val := range map[string]string{
		"statement_timeout": statementTimeout,
		"lock_timeout":      lockTimeout,
	} {
		if q.Get(key) == "" {
			q.Set(key, val)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Soft cap: concurrent misses may race past it by the number of goroutines in flight.
const rebindCacheMax = 1024

var (
	rebindCache  sync.Map
	rebindStored atomic.Int64
)

// Rewrites every `?`: never apply to a query with a literal `?` or to user data.
// Capped because runtime-built queries (IN lists, filter WHERE, patch SET) make the key set
// combinatorial.
func Rebind(query string) string {
	if !strings.ContainsRune(query, '?') {
		return query
	}
	if hit, ok := rebindCache.Load(query); ok {
		if cached, isStr := hit.(string); isStr {
			return cached
		}
	}
	out := rebind(query)
	if rebindStored.Load() < rebindCacheMax {
		if _, loaded := rebindCache.LoadOrStore(query, out); !loaded {
			rebindStored.Add(1)
		}
	}
	return out
}

func rebind(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func CollectStrings(ctx context.Context, db Queryer, q string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
