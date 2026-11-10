package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const (
	DevTokenEnv        = "OCTOPUS_DEV_TOKEN" //nolint:gosec // G101: the name of an env var, not a credential
	CookieDevSession   = "octopus_dev"
	DevTokenQueryParam = "token"
	devTokenFile       = "dev-token"
	minDevTokenLen     = 32
	devSessionTTL      = 30 * 24 * time.Hour
)

var (
	devTokenMu sync.RWMutex
	devToken   string
)

func LoadDevToken(dataDir string) (string, error) {
	if !DevMode() {
		return "", nil
	}
	tok := strings.TrimSpace(os.Getenv(DevTokenEnv))
	if tok != "" && len(tok) < minDevTokenLen {
		return "", apperr.Invalid("auth.errors.dev_token_too_short", "env", DevTokenEnv, "min", minDevTokenLen)
	}
	if tok == "" {
		var err error
		if tok, err = savedDevToken(dataDir); err != nil {
			return "", err
		}
	}
	SetDevToken(tok)
	return tok, nil
}

func savedDevToken(dataDir string) (string, error) {
	if dataDir == "" {
		return "", apperr.Precondition("auth.errors.dev_token_no_data_dir", "env", DevTokenEnv)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", apperr.Wrap(err, "auth.errors.dev_token_create_dir")
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return "", apperr.Wrap(err, "auth.errors.dev_token_open_dir")
	}
	defer func() { logx.Warn(root.Close(), "auth.log.close_dev_token_dir") }()
	if b, err := root.ReadFile(devTokenFile); err == nil {
		if tok := strings.TrimSpace(string(b)); len(tok) >= minDevTokenLen {
			return tok, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", apperr.Wrap(err, "auth.errors.dev_token_read")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", apperr.Wrap(err, "auth.errors.dev_token_generate")
	}
	tok := hex.EncodeToString(b)
	if err := root.WriteFile(devTokenFile, []byte(tok+"\n"), 0o600); err != nil {
		return "", apperr.Wrap(err, "auth.errors.dev_token_save")
	}
	return tok, nil
}

func SetDevToken(tok string) {
	devTokenMu.Lock()
	defer devTokenMu.Unlock()
	devToken = tok
}

func devTokenMatches(v string) bool {
	devTokenMu.RLock()
	want := devToken
	devTokenMu.RUnlock()
	return want != "" && v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(want)) == 1
}

type devSessionKey struct{}

func DevSession(ctx context.Context) bool {
	on, _ := ctx.Value(devSessionKey{}).(bool) //nolint:errcheck // ok-idiom; false is the safe default
	return on && DevMode()
}

func DevSessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !DevMode() {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet {
			if tok := r.URL.Query().Get(DevTokenQueryParam); tok != "" && devTokenMatches(tok) {
				if err := setDevSessionCookies(w, r); err != nil {
					apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Wrap(err, "auth.errors.dev_session_failed"))
					return
				}
				http.Redirect(w, r, withoutDevToken(r), http.StatusSeeOther)
				return
			}
		}
		if ck, err := r.Cookie(CookieDevSession); err == nil && devTokenMatches(ck.Value) {
			r = r.WithContext(context.WithValue(r.Context(), devSessionKey{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

func setDevSessionCookies(w http.ResponseWriter, r *http.Request) error {
	csrf, err := NewCSRFToken()
	if err != nil {
		return err
	}
	devTokenMu.RLock()
	tok := devToken
	devTokenMu.RUnlock()
	writeDevSessionCookies(w, tok, csrf, requestIsSecure(r), int(devSessionTTL.Seconds()))
	return nil
}

func withoutDevToken(r *http.Request) string {
	q := r.URL.Query()
	q.Del(DevTokenQueryParam)
	dest := r.URL.Path
	// "//host" and "/\host" redirect off-site in browsers: fall back to "/".
	if !strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, "//") || strings.HasPrefix(dest, `/\`) {
		dest = "/"
	}
	if enc := q.Encode(); enc != "" {
		dest += "?" + enc
	}
	return dest
}

func DevDashboardURL(base, tok string) string {
	return strings.TrimRight(base, "/") + "/?" + DevTokenQueryParam + "=" + tok
}
