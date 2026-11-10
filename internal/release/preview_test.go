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

func TestPreviewThroughGitHub(t *testing.T) {
	m := validManifest()
	m.Version, m.Channel, m.RollbackTo = "0.3.1-dev.gabc1234", ChannelPreview, ""
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v0.3.0"},
			{"tag_name": "preview-v0.3.1-dev.gabc1234", "prerelease": true},
			{"tag_name": "preview-v0.3.1"},
			{"tag_name": "v0.3.1-dev.gdef5678"},
		})
	})
	mux.HandleFunc("/repos/o/r/releases/tags/preview-v0.3.1-dev.gabc1234", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"assets": []map[string]string{
			{"name": ManifestFile, "url": srv.URL + "/a/m"},
			{"name": BundleFile, "url": srv.URL + "/a/b"},
		}})
	})
	mux.HandleFunc("/a/m", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	mux.HandleFunc("/a/b", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"bundle":1}`)) })
	srv = httptest.NewServer(mux)
	defer srv.Close()
	g := &GitHub{Repo: "o/r", Client: srv.Client(), API: srv.URL}

	ls, err := g.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || ls[0].Version != "0.3.1-dev.gabc1234" || ls[0].Channel != ChannelPreview || ls[1].Channel != ChannelStable {
		t.Fatalf("listing = %+v", ls)
	}

	var gotArgs []string
	cosignOK := true
	orig := Cosign
	defer func() { Cosign = orig }()
	Cosign = func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		if !cosignOK {
			return []byte("no matching signatures"), fmt.Errorf("exit status 1")
		}
		return nil, nil
	}

	got, _, err := FetchManifest(context.Background(), g, nil, "0.3.1-dev.gabc1234")
	if err != nil {
		t.Fatal(err)
	}
	if got.Channel != ChannelPreview {
		t.Errorf("channel %s", got.Channel)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--certificate-identity "+PreviewIdentity) || !strings.Contains(joined, "--certificate-oidc-issuer "+PreviewIssuer) {
		t.Errorf("cosign args = %v", gotArgs)
	}

	cosignOK = false
	if _, _, err := FetchManifest(context.Background(), g, nil, "0.3.1-dev.gabc1234"); !errors.Is(err, ErrBadSignature) {
		t.Errorf("bad preview signature: %v", err)
	}
	Cosign = func(context.Context, ...string) ([]byte, error) { return nil, ErrNoCosign }
	if _, _, err := FetchManifest(context.Background(), g, nil, "0.3.1-dev.gabc1234"); !errors.Is(err, ErrNoCosign) {
		t.Errorf("no cosign: %v", err)
	}
	if err := VerifyPreviewImage(context.Background(), "ghcr.io/x/octopus:preview-main"); err == nil {
		t.Error("preview image by tag accepted")
	}
}

func TestPreviewSignatureCannotClaimARelease(t *testing.T) {
	orig := Cosign
	defer func() { Cosign = orig }()
	Cosign = func(context.Context, ...string) ([]byte, error) { return nil, nil }
	body, _ := validManifest().Encode()
	if _, err := VerifyPreviewManifest(context.Background(), body, []byte("{}")); err == nil {
		t.Error("a stable manifest verified as a preview")
	}
	if IsPreview("0.3.0") || IsPreview("0.3.0-rc.1") || !IsPreview("0.3.1-dev.g0abc123") {
		t.Error("IsPreview")
	}
}

func TestReleaseKeyCannotSignAPreview(t *testing.T) {
	priv, keys := newKey(t)
	m := validManifest()
	m.Channel, m.RollbackTo = ChannelPreview, ""
	body, _ := m.Encode()
	d := &Dir{Root: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(d.Root, m.Version), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{ManifestFile: body, SignatureFile: sign(t, priv, body)} {
		if err := os.WriteFile(filepath.Join(d.Root, m.Version, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := FetchManifest(context.Background(), d, keys, m.Version); err == nil {
		t.Error("release-key preview accepted")
	}
}
