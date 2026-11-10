// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func mustKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := mustKeypair(t)
	in := Claims{LicenseID: "lic-1", Org: "Acme Corp", Edition: "enterprise", Seats: 500, IssuedAt: 100}
	tok, err := Sign(priv, in)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	out, err := Verify(pub, tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out.Org != "Acme Corp" || out.Seats != 500 || out.Edition != "enterprise" {
		t.Errorf("claims round-trip mismatch: %+v", out)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	pub, priv := mustKeypair(t)
	tok, _ := Sign(priv, Claims{Org: "Acme", Seats: 5})
	payload, sig, _ := strings.Cut(tok, ".")
	tampered := flipFirstByte(payload) + "." + sig
	if _, err := Verify(pub, tampered); !errors.Is(err, ErrTampered) {
		t.Errorf("Verify(tampered payload) err = %v, want ErrTampered", err)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	_, priv := mustKeypair(t)
	otherPub, _ := mustKeypair(t)
	tok, _ := Sign(priv, Claims{Org: "Acme"})
	if _, err := Verify(otherPub, tok); !errors.Is(err, ErrTampered) {
		t.Errorf("Verify(wrong key) err = %v, want ErrTampered", err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	pub, _ := mustKeypair(t)
	for _, tok := range []string{"", "nodot", "a.", ".b", "!!.??"} {
		if _, err := Verify(pub, tok); err == nil {
			t.Errorf("Verify(%q) = nil err, want error", tok)
		}
	}
}

func TestEvaluateStates(t *testing.T) {
	pub, priv := mustKeypair(t)
	now := time.Unix(1_000_000, 0)
	grace := 48 * time.Hour

	sign := func(c Claims) string {
		tok, err := Sign(priv, c)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		return tok
	}

	cases := []struct {
		name  string
		token string
		want  State
	}{
		{"perpetual", sign(Claims{Org: "a"}), StateValid},
		{"active", sign(Claims{Org: "a", ExpiresAt: now.Add(time.Hour).Unix()}), StateValid},
		{"within grace", sign(Claims{Org: "a", ExpiresAt: now.Add(-time.Hour).Unix()}), StateGrace},
		{"past grace", sign(Claims{Org: "a", ExpiresAt: now.Add(-72 * time.Hour).Unix()}), StateExpired},
		{"not yet valid", sign(Claims{Org: "a", NotBefore: now.Add(time.Hour).Unix()}), StateNotYetValid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, state, err := Evaluate(pub, c.token, now, grace)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if state != c.want {
				t.Errorf("state = %d, want %d", state, c.want)
			}
		})
	}
}

func TestEvaluateInvalidTokenIsInvalidState(t *testing.T) {
	_, priv := mustKeypair(t)
	tok, _ := Sign(priv, Claims{Org: "a"})
	otherPub, _ := mustKeypair(t)
	_, state, err := Evaluate(otherPub, tok, time.Unix(1, 0), time.Hour)
	if state != StateInvalid || err == nil {
		t.Errorf("Evaluate(wrong key) = %d / %v, want invalid / error", state, err)
	}
}

func TestPublicKeyFromHexRoundTrip(t *testing.T) {
	pub, _ := mustKeypair(t)
	hexKey := hex.EncodeToString(pub)
	got, err := PublicKeyFromHex(hexKey)
	if err != nil {
		t.Fatalf("PublicKeyFromHex: %v", err)
	}
	if !pub.Equal(got) {
		t.Errorf("public key did not round-trip through hex")
	}
	if _, err := PublicKeyFromHex(""); err == nil {
		t.Errorf("PublicKeyFromHex(\"\") = nil err, want error")
	}
	if _, err := PublicKeyFromHex("zz"); err == nil {
		t.Errorf("PublicKeyFromHex(bad hex) = nil err, want error")
	}
}

func TestGenerateKeypairSignVerify(t *testing.T) {
	pubHex, privHex, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	priv, err := PrivateKeyFromHex(privHex)
	if err != nil {
		t.Fatalf("PrivateKeyFromHex: %v", err)
	}
	pub, err := PublicKeyFromHex(pubHex)
	if err != nil {
		t.Fatalf("PublicKeyFromHex: %v", err)
	}
	tok, err := Sign(priv, Claims{Org: "Acme", Edition: "enterprise"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := Verify(pub, tok)
	if err != nil {
		t.Fatalf("Verify with generated keypair: %v", err)
	}
	if got.Org != "Acme" {
		t.Errorf("claims mismatch: %+v", got)
	}
	if _, err := PrivateKeyFromHex("zz"); err == nil {
		t.Error("PrivateKeyFromHex(bad hex) = nil err, want error")
	}
}

func flipFirstByte(s string) string {
	if s == "" {
		return "x"
	}
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}
