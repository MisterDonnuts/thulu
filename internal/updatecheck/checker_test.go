package updatecheck

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/updatecheck/checktest"
)

var manifest = checktest.Manifest

func stableOnly() map[string]bool { return map[string]bool{release.ChannelStable: true} }

func TestCheckOffersNewerSignedReleasesInAllowedChannels(t *testing.T) {
	signer := checktest.NewSigner(t)
	keys := signer.Keys
	src := &checktest.Source{}
	src.Publish(t, signer, manifest("0.1.0", release.ChannelStable), release.ChannelStable)
	src.Publish(t, signer, manifest("0.2.0", release.ChannelStable), release.ChannelStable)
	src.Publish(t, signer, manifest("0.3.0", release.ChannelStable), release.ChannelStable)
	src.Publish(t, signer, manifest("0.4.0-rc.1", release.ChannelPrerelease), release.ChannelPrerelease)

	c := New(Config{Source: src, Channels: stableOnly(), Keys: keys, Current: "0.2.0"})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.State()
	if len(st.Releases) != 1 || st.Releases[0].Version != "0.3.0" {
		t.Fatalf("releases = %+v, want only 0.3.0", st.Releases)
	}
	if st.Releases[0].Notes != "notes 0.3.0" || st.Releases[0].Manifest == nil {
		t.Errorf("release = %+v", st.Releases[0])
	}
	if st.CurrentManifest == nil || st.CurrentManifest.Version != "0.2.0" || len(st.Rejected) != 0 {
		t.Errorf("current manifest %+v, rejected %+v", st.CurrentManifest, st.Rejected)
	}
	if st.LastCheck.IsZero() || st.LastError != "" || st.DevBuild {
		t.Errorf("state = %+v", st)
	}
	if _, ok := c.Release("0.1.0"); ok {
		t.Error("an older version is offered for a change package")
	}
	if r, ok := c.Release("0.3.0"); !ok || r.Manifest.Version != "0.3.0" {
		t.Errorf("Release(0.3.0) = %+v, %v", r, ok)
	}

	opens := src.Opens.Load()
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.Opens.Load() != opens {
		t.Errorf("a verified manifest was fetched again (%d → %d opens)", opens, src.Opens.Load())
	}
}

func TestCheckRejectsWhatTheReleaseKeyDidNotSign(t *testing.T) {
	keys := checktest.NewSigner(t).Keys
	other := checktest.NewSigner(t)
	src := &checktest.Source{}
	src.Publish(t, other, manifest("0.3.0", release.ChannelStable), release.ChannelStable)

	c := New(Config{Source: src, Channels: stableOnly(), Keys: keys, Current: "0.2.0"})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.State()
	if len(st.Releases) != 0 {
		t.Errorf("an unsigned release was offered: %+v", st.Releases)
	}
	if len(st.Rejected) != 1 || st.Rejected[0].Version != "0.3.0" ||
		!strings.Contains(st.Rejected[0].Reason, "signature") {
		t.Errorf("rejected = %+v", st.Rejected)
	}
}

func TestCheckRefusesAChannelTheSignedManifestContradicts(t *testing.T) {
	signer := checktest.NewSigner(t)
	keys := signer.Keys
	src := &checktest.Source{}
	m := manifest("0.3.0-rc.1", release.ChannelPrerelease)
	src.Publish(t, signer, m, release.ChannelStable)

	c := New(Config{Source: src, Channels: stableOnly(), Keys: keys, Current: "0.2.0"})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.State()
	if len(st.Releases) != 0 || len(st.Rejected) != 1 || !strings.Contains(st.Rejected[0].Reason, "prerelease") {
		t.Errorf("releases %+v rejected %+v", st.Releases, st.Rejected)
	}
}

func TestCheckOffMakesNoCall(t *testing.T) {
	c := New(Config{SourceErr: release.ErrSourceOff, Current: "0.2.0"})
	if err := c.Check(context.Background()); !errors.Is(err, release.ErrSourceOff) {
		t.Errorf("Check with lookups off = %v", err)
	}
	st := c.State()
	if !st.Off || st.Source != "" || !st.LastCheck.IsZero() {
		t.Errorf("state = %+v", st)
	}
	if c.Enabled() {
		t.Error("enabled with lookups off")
	}
	done := make(chan struct{})
	go func() { c.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run kept going with lookups off")
	}

	bad := New(Config{SourceErr: errors.New("OCTOPUS_UPDATE_SOURCE=\"ftp://x\": bad"), Current: "0.2.0"})
	if st := bad.State(); st.Off || st.ConfigError == "" {
		t.Errorf("misconfigured state = %+v", st)
	}
}

func TestCheckKeepsTheLastResultWhenTheSourceFails(t *testing.T) {
	signer := checktest.NewSigner(t)
	keys := signer.Keys
	src := &checktest.Source{}
	src.Publish(t, signer, manifest("0.3.0", release.ChannelStable), release.ChannelStable)
	c := New(Config{Source: src, Channels: stableOnly(), Keys: keys, Current: "0.2.0"})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.FailList(errors.New("503 Service Unavailable"))
	if err := c.Check(context.Background()); err == nil {
		t.Fatal("a failing source reported success")
	}
	st := c.State()
	if len(st.Releases) != 1 || !strings.Contains(st.LastError, "503") {
		t.Errorf("state after a failed check = %+v", st)
	}
}

func TestCheckCapsManifestFetches(t *testing.T) {
	signer := checktest.NewSigner(t)
	keys := signer.Keys
	src := &checktest.Source{}
	for _, v := range []string{"0.3.0", "0.4.0", "0.5.0", "0.6.0"} {
		src.Publish(t, signer, manifest(v, release.ChannelStable), release.ChannelStable)
	}
	c := New(Config{Source: src, Channels: stableOnly(), Keys: keys, Current: "0.2.0", MaxManifests: 2})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.State()
	if len(st.Releases) != 2 || st.Releases[0].Version != "0.6.0" || st.Releases[1].Version != "0.5.0" {
		t.Errorf("releases = %+v, want the newest two", st.Releases)
	}
}

func TestDevBuildListsReleases(t *testing.T) {
	signer := checktest.NewSigner(t)
	keys := signer.Keys
	src := &checktest.Source{}
	src.Publish(t, signer, manifest("0.3.0", release.ChannelStable), release.ChannelStable)
	for _, cur := range []string{"dev", "0.2.0-dev.abc1234"} {
		c := New(Config{Source: src, Channels: stableOnly(), Keys: keys, Current: cur})
		if err := c.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		st := c.State()
		if !st.DevBuild || len(st.Releases) != 1 {
			t.Errorf("%s: state = %+v", cur, st)
		}
	}
}

func TestIntervalFromEnv(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"":      DefaultInterval,
		"6h":    6 * time.Hour,
		"5m":    MinInterval,
		"often": DefaultInterval,
	} {
		got := IntervalFromEnv(func(string) string { return in })
		if got != want {
			t.Errorf("%q = %s, want %s", in, got, want)
		}
	}
}

func TestFromEnvOff(t *testing.T) {
	c, err := FromEnv(func(k string) string {
		if k == release.EnvSource {
			return "off"
		}
		return ""
	}, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if st := c.State(); !st.Off || len(st.Channels) != 1 || st.Channels[0] != release.ChannelStable {
		t.Errorf("state = %+v", st)
	}
}

func TestPreviewWithoutCosignIsListedUnverified(t *testing.T) {
	orig := release.Cosign
	t.Cleanup(func() { release.Cosign = orig })
	release.Cosign = func(context.Context, ...string) ([]byte, error) { return nil, release.ErrNoCosign }

	src := &checktest.Source{}
	src.PublishPreview(t, "0.3.1-dev.gabc1234")
	channels := map[string]bool{release.ChannelStable: true, release.ChannelPreview: true}
	c := New(Config{Source: src, Channels: channels, Keys: checktest.NewSigner(t).Keys, Current: "0.3.0"})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.State()
	if len(st.Releases) != 1 || st.Releases[0].Verified() || st.Releases[0].Channel != release.ChannelPreview ||
		st.Releases[0].Notes != "notes 0.3.1-dev.gabc1234" || len(st.Rejected) != 0 {
		t.Fatalf("state = %+v", st)
	}
	if _, ok := c.Release("0.3.1-dev.gabc1234"); ok {
		t.Error("a change package is offered for an unverified preview")
	}

	release.Cosign = func(context.Context, ...string) ([]byte, error) {
		return []byte("bad signature"), errors.New("exit status 1")
	}
	c = New(Config{Source: src, Channels: channels, Keys: checktest.NewSigner(t).Keys, Current: "0.3.0"})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := c.State(); len(st.Releases) != 0 || len(st.Rejected) != 1 {
		t.Errorf("bad preview signature: %+v", st)
	}

	opens := src.Opens.Load()
	c = New(Config{Source: src, Channels: stableOnly(), Keys: checktest.NewSigner(t).Keys, Current: "0.3.0"})
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := c.State(); len(st.Releases) != 0 || src.Opens.Load() != opens {
		t.Errorf("preview fetched without the channel allowed: %+v", st)
	}
}
