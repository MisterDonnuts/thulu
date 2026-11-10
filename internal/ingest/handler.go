package ingest

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/middleware"
)

const maxBodyBytes = 4 << 20

const requestsPerMinute = 600

type Decoder interface {
	Decode(envID, kind, contentType string, body []byte) error
}

type Handler struct {
	store   *Store
	decoder Decoder

	mu      sync.Mutex
	buckets map[string]*rateBucket
}

type rateBucket struct {
	count  int
	window time.Time
}

func NewHandler(store *Store, decoder Decoder) *Handler {
	return &Handler{store: store, decoder: decoder, buckets: map[string]*rateBucket{}}
}

// SDKs with OTEL_EXPORTER_OTLP_ENDPOINT=https://host/ingest append /v1/<kind> themselves; these
// paths must match.
var Kinds = []string{"traces", "metrics", "logs"}

// Deliberately outside the normal API auth: these routes take an ingest token, and the API key gate
// misbehaves on paths overlapping the SPA.
func (h *Handler) Register(mux *http.ServeMux) {
	for _, kind := range Kinds {
		mux.HandleFunc("POST /ingest/v1/"+kind, h.serve(kind))
	}
}

func (h *Handler) ServeKind(kind string, w http.ResponseWriter, r *http.Request) {
	h.serve(kind)(w, r)
}

func (h *Handler) serve(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientIP := middleware.ClientIP(r)
		raw := bearerToken(r)

		// Rate-limit on the raw token before lookup so forged tokens cannot each cost a database
		// read.
		if !h.allow(rateKey(raw, clientIP)) {
			// 429 is the one distinct refusal: it is keyed before lookup, so it reveals nothing
			// about the token's validity.
			w.Header().Set("Retry-After", "60")
			apperr.WriteJSON(w, r, http.StatusTooManyRequests, apperr.RateLimited("ingest.errors.rate_limited"))
			return
		}

		res := h.store.Authenticate(r.Context(), raw, clientIP)
		if !res.OK {
			// Every rejection must answer identically; "IP not allowed" would tell a prober the
			// token is valid. The reason goes to the audit view only.
			deny(w)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			apperr.WriteJSON(w, r, http.StatusRequestEntityTooLarge, apperr.TooLarge("ingest.errors.payload_too_large"))
			return
		}

		if h.decoder != nil {
			if err := h.decoder.Decode(res.EnvironmentID, kind, r.Header.Get("Content-Type"), body); err != nil {
				// 503, not 4xx: OTLP clients retry 5xx and permanently drop data on 4xx.
				logx.Warn(err, "ingest.log.decode_failed", "kind", kind, "env", logx.Safe(res.EnvironmentID))
				apperr.WriteJSON(w, r, http.StatusServiceUnavailable, apperr.Unavailable("ingest.errors.process_failed"))
				return
			}
		}

		w.WriteHeader(http.StatusOK)
	}
}

// Also used by instances with ingest disabled, so scanners cannot tell which deployments have the
// feature.
func Deny(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
}

func deny(w http.ResponseWriter) { Deny(w) }

func bearerToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Octopus-Token")); v != "" {
		return v
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func rateKey(rawToken, ip string) string {
	if rawToken != "" {
		return "t:" + hashToken(rawToken)
	}
	return "ip:" + ip
}

func (h *Handler) allow(key string) bool {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.buckets[key]
	if b == nil || now.Sub(b.window) >= time.Minute {
		h.buckets[key] = &rateBucket{count: 1, window: now}
		if len(h.buckets) > 10000 {
			for k, v := range h.buckets {
				if now.Sub(v.window) >= time.Minute {
					delete(h.buckets, k)
				}
			}
		}
		return true
	}
	if b.count >= requestsPerMinute {
		return false
	}
	b.count++
	return true
}
