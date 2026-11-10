package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"

	"github.com/MisterDonnuts/thulu/internal/middleware"
)

type spaHandler struct {
	root fs.FS
}

func newSPAHandler(root fs.FS) *spaHandler {
	sub, err := fs.Sub(root, "dist")
	if err != nil {
		panic("spaHandler: " + err.Error())
	}
	return &spaHandler{root: sub}
}

// Overlap with API paths (e.g. /agents) is safe: API routes are gated by RequireRole independently.
func (h *spaHandler) IsAsset(reqPath string) bool {
	name := strings.Trim(path.Clean("/"+reqPath), "/")
	if name == "" {
		return true
	}
	_, err := fs.Stat(h.root, name)
	return err == nil
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Without this, a write to an unknown endpoint falls through here and gets 200 with the
	// dashboard HTML.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeErrKey(w, r, http.StatusNotFound, "api.static.errors.no_such_endpoint", "method", r.Method, "path", r.URL.Path)
		return
	}

	// No page lives under /auth/, so even a GET here is an unknown API call and must not get the
	// HTML shell.
	if strings.HasPrefix(r.URL.Path, "/auth/") {
		writeErrKey(w, r, http.StatusNotFound, "api.static.errors.no_such_endpoint", "method", r.Method, "path", r.URL.Path)
		return
	}

	w.Header().Set("Content-Security-Policy", middleware.CSPWebApp)

	r2 := r.Clone(r.Context())
	r2.URL.Path = h.localized(h.servePath(r.URL.Path), i18n.RequestLocale(r))
	if !strings.HasPrefix(r2.URL.Path, "/"+hashedAssetDir+"/") {
		//i18n:ignore HTTP header value, not a message
		w.Header().Add("Vary", "Accept-Language, Cookie")
	}
	setCacheControl(w, r2.URL.Path)
	http.FileServerFS(h.root).ServeHTTP(w, r2)
}

// The HTML shell is the only file whose name survives a deploy and it names the fingerprinted
// bundles, so it must revalidate; _astro assets are immutable.
func setCacheControl(w http.ResponseWriter, servePath string) {
	if strings.HasPrefix(servePath, "/"+hashedAssetDir+"/") {
		//i18n:ignore HTTP header value, not a message
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
}

const hashedAssetDir = "_astro"

// Must match the locale output dir of web/scripts/build-locales.mjs.
const localeDir = "_locales"

func (h *spaHandler) localized(servePath, locale string) string {
	if locale == i18n.SourceLocale || strings.HasPrefix(servePath, "/"+hashedAssetDir+"/") {
		return servePath
	}
	candidate := "/" + localeDir + "/" + locale + servePath
	name := strings.Trim(candidate, "/")
	if strings.HasSuffix(candidate, "/") {
		name += "/index.html"
	}
	if _, err := fs.Stat(h.root, name); err != nil {
		return servePath
	}
	return candidate
}

func (h *spaHandler) servePath(reqPath string) string {
	name := strings.Trim(path.Clean("/"+reqPath), "/")
	if name == "" {
		return "/"
	}

	info, err := fs.Stat(h.root, name)
	if err != nil {
		return "/"
	}
	if info.IsDir() {
		return "/" + name + "/"
	}
	return "/" + name
}
