package middleware

import (
	"errors"
	"log"
	"net/http"
	"runtime/debug"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Must be the outermost middleware so it also catches panics inside other middleware.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler must reach the server to abort the response; re-panic it.
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			reqID := w.Header().Get("X-Request-ID")
			//nolint:gosec // G706: method/path sanitized with logx.Safe; reqID is server-generated; stack is trusted
			log.Printf("[panic] %s", i18n.Log("middleware.log.panic",
				"method", logx.Safe(r.Method), "path", logx.Safe(r.URL.Path), "request_id", reqID,
				"panic", rec, "stack", string(debug.Stack())))
			// A write on a hijacked or flushed conn may itself panic; swallow it.
			defer func() { _ = recover() }() //nolint:errcheck // intentional: discard nested panic
			apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("middleware.errors.internal"))
		}()
		next.ServeHTTP(w, r)
	})
}
