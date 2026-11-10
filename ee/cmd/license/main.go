// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

// license issues Thulu Enterprise licenses, signed with an ed25519 key.
//
//	go run ./ee/cmd/license keygen
//	OCTOPUS_LICENSE_SIGNING_KEY=<private hex> go run ./ee/cmd/license \
//	  -licensee "Acme Corp" -features "*" [-expires 2027-01-01]
//
// The daemon verifies the token offline against the public key in ee/licensing; it never phones home.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/ee/licensing"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func main() {
	configureLocale()

	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		pub, priv, err := licensing.GenerateKeypair()
		if err != nil {
			fail(err)
		}
		fmt.Println(i18n.Text("license.cli.keypair_title"))
		fmt.Println()
		fmt.Println(i18n.Text("license.cli.keypair_public", "key", pub))
		fmt.Println(i18n.Text("license.cli.keypair_private", "key", priv))
		return
	}

	licensee := flag.String("licensee", "", i18n.Text("license.cli.flag_licensee"))
	features := flag.String("features", "*", i18n.Text("license.cli.flag_features"))
	expires := flag.String("expires", "", i18n.Text("license.cli.flag_expires"))
	edition := flag.String("edition", "enterprise", i18n.Text("license.cli.flag_edition"))
	seats := flag.Int("seats", 0, i18n.Text("license.cli.flag_seats"))
	flag.Parse()

	if *licensee == "" {
		fail(apperr.Invalid("license.errors.licensee_required"))
	}
	signingKey := os.Getenv("OCTOPUS_LICENSE_SIGNING_KEY")
	if signingKey == "" {
		fail(apperr.Invalid("license.errors.signing_key_missing"))
	}
	priv, err := licensing.PrivateKeyFromHex(signingKey)
	if err != nil {
		fail(err)
	}

	featureList := strings.Split(*features, ",")
	for i, f := range featureList {
		featureList[i] = strings.TrimSpace(f)
	}

	var expAt int64
	if *expires != "" {
		t, parseErr := time.ParseInLocation("2006-01-02", *expires, time.UTC)
		if parseErr != nil {
			fail(apperr.WrapKind(apperr.KindInvalid, parseErr, "license.errors.invalid_date", "date", *expires))
		}
		expAt = t.Unix()
	}

	token, err := licensing.Sign(priv, licensing.Claims{
		Org:       *licensee,
		Edition:   *edition,
		Features:  featureList,
		Seats:     *seats,
		IssuedAt:  time.Now().Unix(),
		ExpiresAt: expAt,
	})
	if err != nil {
		fail(err)
	}
	fmt.Println(i18n.Text("license.cli.licensee", "licensee", *licensee))
	fmt.Println(i18n.Text("license.cli.edition", "edition", *edition))
	fmt.Println(i18n.Text("license.cli.features", "features", strings.Join(featureList, ", ")))
	if *seats > 0 {
		fmt.Println(i18n.Text("license.cli.seats", "count", *seats))
	}
	if expAt > 0 {
		fmt.Println(i18n.Text("license.cli.expires", "date", time.Unix(expAt, 0).UTC().Format("2006-01-02")))
	} else {
		fmt.Println(i18n.Text("license.cli.expires_never"))
	}
	fmt.Println()
	fmt.Println("OCTOPUS_LICENSE_KEY=" + token)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, i18n.Text("license.cli.error", "err", apperr.Text(err)))
	os.Exit(1)
}

func configureLocale() {
	if v := os.Getenv("OCTOPUS_LOCALE"); v != "" {
		if !i18n.SetDaemonLocale(v) {
			fmt.Fprintln(os.Stderr, i18n.Log("cli.log.unknown_locale", "fallback", i18n.SourceLocale))
		}
		return
	}
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			i18n.SetDaemonLocale(i18n.Negotiate(v))
			return
		}
	}
}
