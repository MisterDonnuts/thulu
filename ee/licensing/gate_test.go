// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package licensing

import (
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	pub, priv := mustKeypair(t)
	pubHex := hexEnc(pub)
	now := time.Unix(2_000_000, 0)
	grace := DefaultGrace

	sign := func(c Claims) string {
		tok, err := Sign(priv, c)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		return tok
	}
	valid := sign(Claims{Org: "Acme", Edition: "enterprise", ExpiresAt: now.Add(time.Hour).Unix()})
	otherPub, _ := mustKeypair(t)

	cases := []struct {
		name     string
		pubHex   string
		token    string
		licensed bool
		state    State
	}{
		{"no token", pubHex, "", false, StateInvalid},
		{"no vendor key in the build", "", valid, false, StateInvalid},
		{"valid", pubHex, valid, true, StateValid},
		{"expired within grace", pubHex, sign(Claims{Org: "Acme", ExpiresAt: now.Add(-time.Hour).Unix()}), true, StateGrace},
		{"expired past grace", pubHex, sign(Claims{Org: "Acme", ExpiresAt: now.Add(-2 * grace).Unix()}), false, StateExpired},
		{"not yet valid", pubHex, sign(Claims{Org: "Acme", NotBefore: now.Add(time.Hour).Unix()}), false, StateNotYetValid},
		{"tampered", pubHex, valid + "x", false, StateInvalid},
		{"signed by another vendor", hexEnc(otherPub), valid, false, StateInvalid},
	}
	for _, c := range cases {
		d := Resolve(c.pubHex, c.token, now, grace)
		if d.Licensed != c.licensed || d.State != c.state {
			t.Errorf("%s: got licensed=%v state=%v, want licensed=%v state=%v (%s)",
				c.name, d.Licensed, d.State, c.licensed, c.state, d.Reason)
		}
		if d.Reason == "" {
			t.Errorf("%s: empty reason", c.name)
		}
	}
}

func hexEnc(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}
