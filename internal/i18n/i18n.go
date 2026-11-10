package i18n

import (
	"context"
	"embed"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

const SourceLocale = "en"

// Must match the cookie name the web dashboard sets.
const LocaleCookie = "octopus_locale"

//go:embed locales
var localesFS embed.FS

var catalog = mustLoad()

func mustLoad() *Catalog {
	c, err := Load(localesFS)
	if err != nil {
		panic(err)
	}
	return c
}

var daemonLocale atomic.Pointer[string]

func init() { setDaemonLocale(SourceLocale) }

func setDaemonLocale(l string) { daemonLocale.Store(&l) }

func ReplaceForTest(c *Catalog) (restore func()) {
	prev := catalog
	catalog = c
	return func() { catalog = prev }
}

func Default() *Catalog { return catalog }

func SetDaemonLocale(locale string) bool {
	if l, ok := match(locale); ok {
		setDaemonLocale(l)
		return true
	}
	setDaemonLocale(SourceLocale)
	return locale == ""
}

func DaemonLocale() string { return *daemonLocale.Load() }

func T(locale, key string, args ...any) string { return catalog.Render(locale, key, args...) }

func TCtx(ctx context.Context, key string, args ...any) string {
	return catalog.Render(FromContext(ctx), key, args...)
}

func Text(key string, args ...any) string { return catalog.Render(DaemonLocale(), key, args...) }

// Always English, whatever OCTOPUS_LOCALE says: operators grep logs and alerts match on them.
func Log(key string, args ...any) string { return catalog.Render(SourceLocale, key, args...) }

func UsesCause(locale, key string) bool {
	tmpls := catalog.Templates(locale, key)
	if len(tmpls) == 0 && locale != SourceLocale {
		tmpls = catalog.Templates(SourceLocale, key) // Must mirror Render's fallback to the source locale.
	}
	for _, t := range tmpls {
		if strings.Contains(t, "{"+causeVar+"}") {
			return true
		}
	}
	return false
}

// apperr fills {cause} with the wrapped error's message; keep in sync with apperr.
const causeVar = "cause"

type ctxKey struct{}

func WithLocale(ctx context.Context, locale string) context.Context {
	return context.WithValue(ctx, ctxKey{}, locale)
}

func FromContext(ctx context.Context) string {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(string); ok && l != "" {
			return l
		}
	}
	return DaemonLocale()
}

func FromRequest(r *http.Request) string {
	if c, err := r.Cookie(LocaleCookie); err == nil {
		if l, ok := match(c.Value); ok {
			return l
		}
	}
	if l := Negotiate(r.Header.Get("Accept-Language")); l != "" {
		return l
	}
	return DaemonLocale()
}

func RequestLocale(r *http.Request) string {
	if r == nil {
		return DaemonLocale()
	}
	if l, ok := r.Context().Value(ctxKey{}).(string); ok && l != "" {
		return l
	}
	return FromRequest(r)
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithLocale(r.Context(), FromRequest(r))))
	})
}

func Negotiate(header string) string {
	type pref struct {
		tag string
		q   float64
	}
	var prefs []pref
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.TrimSpace(fields[0])
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(f), "q="); ok {
				if parsed, err := strconv.ParseFloat(v, 64); err == nil {
					q = parsed
				}
			}
		}
		if q > 0 {
			prefs = append(prefs, pref{tag, q})
		}
	}
	sort.SliceStable(prefs, func(i, j int) bool { return prefs[i].q > prefs[j].q })
	for _, p := range prefs {
		if l, ok := match(p.tag); ok {
			return l
		}
	}
	return ""
}

func match(tag string) (string, bool) {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	base, _, _ = strings.Cut(base, "_")
	if base == "" {
		return "", false
	}
	if _, ok := catalog.locales[base]; ok {
		return base, true
	}
	return "", false
}
