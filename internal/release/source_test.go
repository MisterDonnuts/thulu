package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSourceFromEnv(t *testing.T) {
	s, err := SourceFromEnv(env(nil), nil)
	if err != nil || s.Name() != "github "+DefaultRepo {
		t.Fatalf("default = %v, %v", s, err)
	}
	if _, err := SourceFromEnv(env(map[string]string{EnvSource: "off"}), nil); !errors.Is(err, ErrSourceOff) {
		t.Errorf("off = %v", err)
	}
	s, err = SourceFromEnv(env(map[string]string{EnvSource: "https://mirror.bank.example/octopus/"}), nil)
	if err != nil || s.Name() != "https://mirror.bank.example/octopus" {
		t.Errorf("mirror = %v, %v", s, err)
	}
	for _, bad := range []string{"ftp://x", "https://user:pw@x", "https://x/?token=1", "mirror", "https://"} {
		if _, err := SourceFromEnv(env(map[string]string{EnvSource: bad}), nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSourceFromEnvKeepsCredentialsOutOfTheError(t *testing.T) {
	for _, bad := range []string{
		"https://svc:hunter2@mirror.bank.example/octopus",
		"https://bucket.s3.amazonaws.com/octopus?X-Amz-Signature=hunter2",
		"https://mirror.bank.example/octopus?token=hunter2#hunter2",
	} {
		_, err := SourceFromEnv(env(map[string]string{EnvSource: bad}), nil)
		if err == nil {
			t.Fatalf("%q accepted", bad)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error leaks the credential: %v", err)
		}
		if !strings.Contains(err.Error(), "REDACTED") {
			t.Errorf("error does not say what was removed: %v", err)
		}
	}
}

func TestChannelsFromEnv(t *testing.T) {
	c := ChannelsFromEnv(env(nil))
	if !c[ChannelStable] || c[ChannelPrerelease] || c[ChannelPreview] {
		t.Errorf("default = %v", c)
	}
	c = ChannelsFromEnv(env(map[string]string{EnvChannels: "prerelease, preview,nightly"}))
	if !c[ChannelStable] || !c[ChannelPrerelease] || !c[ChannelPreview] || c["nightly"] {
		t.Errorf("all = %v", c)
	}
}

func signedRelease(t *testing.T, keysOut *Keys, version string) ([]byte, []byte) {
	t.Helper()
	priv, keys := newKey(t)
	*keysOut = keys
	m := validManifest()
	m.Version = version
	m.RollbackTo = ""
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b, sign(t, priv, b)
}

func TestGitHubSource(t *testing.T) {
	var keys Keys
	body, sig := signedRelease(t, &keys, "0.3.0")
	var sawAuth string
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v0.2.0", "published_at": "2026-09-01T00:00:00Z"},
			{"tag_name": "v0.3.0", "body": "notes", "published_at": "2026-10-01T00:00:00Z"},
			{"tag_name": "v0.4.0-rc.1", "prerelease": true, "published_at": "2026-10-02T00:00:00Z"},
			{"tag_name": "v9.9.9", "draft": true},
			{"tag_name": "nightly"},
		})
	})
	mux.HandleFunc("/repos/o/r/releases/tags/v0.3.0", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.3.0", "assets": []map[string]string{
			{"name": ManifestFile, "url": srv.URL + "/assets/1"},
			{"name": SignatureFile, "url": srv.URL + "/assets/2"},
		}})
	})
	mux.HandleFunc("/assets/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "accept", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/assets/2", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(sig) })
	srv = httptest.NewServer(mux)
	defer srv.Close()

	g := &GitHub{Repo: "o/r", Token: "t0ken", Client: srv.Client(), API: srv.URL}
	ls, err := g.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range ls {
		got = append(got, l.Version+"/"+l.Channel)
	}
	if strings.Join(got, ",") != "0.4.0-rc.1/prerelease,0.3.0/stable,0.2.0/stable" {
		t.Errorf("listing = %v", got)
	}
	if sawAuth != "Bearer t0ken" {
		t.Errorf("authorization = %q", sawAuth)
	}
	m, _, err := FetchManifest(context.Background(), g, keys, "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "0.3.0" {
		t.Errorf("manifest version %s", m.Version)
	}
	if _, err := g.Open(context.Background(), "0.3.0", "../x"); err == nil {
		t.Error("path in asset name accepted")
	}
}

func TestMirrorRefusesAManifestOfAnotherVersion(t *testing.T) {
	var keys Keys
	body, sig := signedRelease(t, &keys, "0.2.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + IndexFile:
			_, _ = fmt.Fprint(w, `{"releases":[{"version":"0.2.0","channel":"stable"},{"version":"0.3.0","channel":"stable"}]}`)
		case "/0.3.0/" + ManifestFile:
			_, _ = w.Write(body)
		case "/0.3.0/" + SignatureFile:
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := &Mirror{Base: srv.URL, Client: srv.Client()}
	ls, err := m.List(context.Background())
	if err != nil || len(ls) != 2 || ls[0].Version != "0.3.0" {
		t.Fatalf("list = %v, %v", ls, err)
	}
	if _, _, err := FetchManifest(context.Background(), m, keys, "0.3.0"); err == nil || !strings.Contains(err.Error(), "is for 0.2.0") {
		t.Errorf("downgrade under another name: %v", err)
	}
	if _, _, err := FetchManifest(context.Background(), m, keys, "0.2.0"); err == nil {
		t.Error("missing files accepted")
	}
}

func TestDirSource(t *testing.T) {
	var keys Keys
	body, sig := signedRelease(t, &keys, "0.3.0")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "0.3.0"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		IndexFile:                             []byte(`{"releases":[{"version":"0.3.0","channel":"stable"}]}`),
		filepath.Join("0.3.0", ManifestFile):  body,
		filepath.Join("0.3.0", SignatureFile): sig,
	} {
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := &Dir{Root: root}
	if _, _, err := FetchManifest(context.Background(), d, keys, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Open(context.Background(), "../..", ManifestFile); err == nil {
		t.Error("path as version accepted")
	}
}
