package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var jwtSecret []byte

// The warning is deferred to ReportSecretPosture: init runs on import, so CLI commands that never
// issue sessions would log it.
var ephemeralSecret bool

var shortSecret bool

func init() {
	raw := strings.TrimSpace(os.Getenv("OCTOPUS_JWT_SECRET"))
	if raw == "" {
		b := make([]byte, 32)
		rand.Read(b)
		jwtSecret = b
		ephemeralSecret = true
		return
	}
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) >= 16 {
		jwtSecret = decoded
	} else {
		jwtSecret = []byte(raw)
	}
	shortSecret = len(jwtSecret) < 16
}

func ReportSecretPosture() {
	switch {
	case ephemeralSecret:
		log.Printf("[auth] %s", i18n.Log("auth.log.jwt_secret_ephemeral"))
	case shortSecret:
		log.Printf("[auth] %s", i18n.Log("auth.log.jwt_secret_short"))
	}
}

type Claims struct {
	UserID       string    `json:"sub"`
	Email        string    `json:"email"`
	WorkspaceID  string    `json:"wid"`
	Role         Role      `json:"role"`
	TokenVersion int       `json:"tv"`
	IssuedAt     time.Time `json:"iat"`
	ExpiresAt    time.Time `json:"exp"`
}

const jwtHeader = `{"alg":"HS256","typ":"JWT"}`

// Short on purpose: if the per-request token_version recheck is bypassed, a leaked access token
// still dies quickly.
const defaultAccessTTL = 30 * time.Minute

func AccessTTL() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("OCTOPUS_ACCESS_TTL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		log.Printf("[auth] %s", i18n.Log("auth.log.access_ttl_invalid", "default", defaultAccessTTL))
	}
	return defaultAccessTTL
}

func IssueJWT(c Claims) (string, error) {
	if c.IssuedAt.IsZero() {
		c.IssuedAt = time.Now().UTC()
	}
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = c.IssuedAt.Add(AccessTTL())
	}

	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(jwtHeader))
	payloadJSON, err := json.Marshal(c)
	if err != nil {
		return "", apperr.Wrap(err, "auth.errors.marshal_claims")
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	signing := headerB64 + "." + payloadB64
	sig := sign([]byte(signing))

	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func ValidateJWT(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, apperr.Unauthenticated("auth.errors.token_format_invalid")
	}

	signing := parts[0] + "." + parts[1]
	expectedSig := sign([]byte(signing))

	gotSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, apperr.Unauthenticated("auth.errors.signature_encoding_invalid")
	}
	if !hmac.Equal(expectedSig, gotSig) {
		return nil, apperr.Unauthenticated("auth.errors.signature_invalid")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, apperr.Unauthenticated("auth.errors.payload_encoding_invalid")
	}

	var c Claims
	if err := json.Unmarshal(payloadJSON, &c); err != nil {
		return nil, apperr.WrapKind(apperr.KindUnauthenticated, err, "auth.errors.payload_json_invalid")
	}

	if time.Now().After(c.ExpiresAt) {
		return nil, apperr.Unauthenticated("auth.errors.token_expired")
	}

	return &c, nil
}

func IssueRefreshToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", apperr.Wrap(err, "auth.errors.random_failed")
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(h[:])
	return token, hash, nil
}

func HashRefreshToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func sign(data []byte) []byte {
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write(data)
	return mac.Sum(nil)
}

// openEnvelope does not check expiry: each payload carries its own, and the caller must check it.

func signEnvelope(payload any) (string, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payloadJSON)
	return encoded + "." + hex.EncodeToString(sign([]byte(encoded))), nil
}

func openEnvelope(token string, dst any) error {
	encoded, gotSig, found := strings.Cut(token, ".")
	if !found {
		return apperr.Unauthenticated("auth.errors.envelope_format_invalid")
	}
	expected := hex.EncodeToString(sign([]byte(encoded)))
	if !hmac.Equal([]byte(expected), []byte(gotSig)) {
		return apperr.Unauthenticated("auth.errors.signature_invalid")
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return apperr.Unauthenticated("auth.errors.envelope_encoding_invalid")
	}
	if err := json.Unmarshal(payloadJSON, dst); err != nil {
		return apperr.Unauthenticated("auth.errors.envelope_payload_invalid")
	}
	return nil
}
