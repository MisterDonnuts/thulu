// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Mister.D's license signing key. Replacing it invalidates every license already issued.
var VendorPublicKeyHex = "ee3ff3b1a516839ad8ab23feb1a418199310f6ffd121f17bde00c87bfd6ea672"

type Claims struct {
	LicenseID  string   `json:"lid"`
	Org        string   `json:"org"`
	Edition    string   `json:"edition"`
	Features   []string `json:"features,omitempty"`
	Seats      int      `json:"seats,omitempty"`
	IssuedAt   int64    `json:"iat"`
	NotBefore  int64    `json:"nbf,omitempty"`
	ExpiresAt  int64    `json:"exp,omitempty"`
	VersionMin string   `json:"vmin,omitempty"`
	VersionMax string   `json:"vmax,omitempty"`
}

type State int

const (
	StateInvalid State = iota
	StateNotYetValid
	StateValid
	StateGrace
	StateExpired
)

var (
	ErrMalformed error = apperr.Invalid("licensing.errors.malformed_token")
	ErrTampered  error = apperr.Invalid("licensing.errors.signature_mismatch")
)

func Sign(priv ed25519.PrivateKey, c Claims) (string, error) {
	payloadJSON, err := json.Marshal(c)
	if err != nil {
		return "", apperr.Wrap(err, "licensing.errors.marshal_claims")
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	sig := ed25519.Sign(priv, []byte(payloadB64))
	return payloadB64 + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Does not check expiry or not-before; use Evaluate for that.
func Verify(pub ed25519.PublicKey, token string) (*Claims, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, apperr.Invalid("licensing.errors.public_key_size", "size", len(pub), "want", ed25519.PublicKeySize)
	}
	payloadB64, sigB64, ok := strings.Cut(token, ".")
	if !ok || payloadB64 == "" || sigB64 == "" {
		return nil, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, ErrMalformed
	}
	if !ed25519.Verify(pub, []byte(payloadB64), sig) {
		return nil, ErrTampered
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(payloadJSON, &c); err != nil {
		return nil, ErrMalformed
	}
	return &c, nil
}

func Evaluate(pub ed25519.PublicKey, token string, now time.Time, grace time.Duration) (*Claims, State, error) {
	c, err := Verify(pub, token)
	if err != nil {
		return nil, StateInvalid, err
	}
	if c.NotBefore != 0 && now.Before(time.Unix(c.NotBefore, 0)) {
		return c, StateNotYetValid, nil
	}
	if c.ExpiresAt == 0 {
		return c, StateValid, nil
	}
	exp := time.Unix(c.ExpiresAt, 0)
	switch {
	case now.Before(exp):
		return c, StateValid, nil
	case now.Before(exp.Add(grace)):
		return c, StateGrace, nil
	default:
		return c, StateExpired, nil
	}
}

func GenerateKeypair() (pubHex, privHex string, err error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", apperr.Wrap(err, "licensing.errors.generate_keypair")
	}
	return hex.EncodeToString(pub), hex.EncodeToString(priv), nil
}

func PrivateKeyFromHex(h string) (ed25519.PrivateKey, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(h))
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindInvalid, err, "licensing.errors.decode_private_key")
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, apperr.Invalid("licensing.errors.private_key_size", "size", len(raw), "want", ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

func PublicKeyFromHex(h string) (ed25519.PublicKey, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return nil, apperr.Invalid("licensing.errors.no_public_key")
	}
	raw, err := hex.DecodeString(h)
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindInvalid, err, "licensing.errors.decode_public_key")
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, apperr.Invalid("licensing.errors.public_key_size", "size", len(raw), "want", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}
