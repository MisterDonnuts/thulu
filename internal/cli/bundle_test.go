//go:build !hostcli

package cli

import (
	"bytes"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/stage"
)

func TestPickImportTarget(t *testing.T) {
	nueva := []workspaceRow{{id: "bbbb000000000005", slug: "nueva"}}
	two := append(slices.Clone(nueva), workspaceRow{id: "bbbb000000000008", slug: "acme"})
	fb := stage.FallbackWorkspaceID

	cases := []struct {
		name   string
		rows   []workspaceRow
		source string
		named  string
		inUse  bool
		want   string
		kind   apperr.Kind
	}{
		{"named by slug", two, fb, "acme", false, "bbbb000000000008", apperr.KindUnspecified},
		{"source exists", two, "bbbb000000000008", "", false, "bbbb000000000008", apperr.KindUnspecified},
		{"dev bundle, one workspace", nueva, fb, "", false, "bbbb000000000005", apperr.KindUnspecified},
		{"dev bundle, dev install", nil, fb, "", false, fb, apperr.KindUnspecified},
		{"dev bundle, fallback still holds data", nueva, fb, "", true, fb, apperr.KindUnspecified},
		{"dev bundle, several workspaces", two, fb, "", false, "", apperr.KindNotFound},
		{"other install's workspace, one here", nueva, "deadbeefdeadbeef", "", false, "bbbb000000000005", apperr.KindUnspecified},
		{"other install's workspace, several here", two, "deadbeefdeadbeef", "", false, "", apperr.KindNotFound},
		{"other install's workspace, none here", nil, "deadbeefdeadbeef", "", false, "", apperr.KindNotFound},
	}
	for _, c := range cases {
		got, err := pickImportTarget(c.rows, c.source, c.named, c.inUse)
		if c.kind != apperr.KindUnspecified {
			if err == nil || apperr.KindOf(err) != c.kind {
				t.Errorf("%s: got %q, %v; want kind %d", c.name, got, err, c.kind)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestExportAsksWhenTheFallbackHoldsData(t *testing.T) {
	acme := []workspaceRow{{id: "bbbb000000000008", slug: "acme"}}
	if got, err := pickWorkspace(withFallback(acme, false), "", true); err != nil || got != "bbbb000000000008" {
		t.Errorf("unused fallback: got %q, %v", got, err)
	}
	_, err := pickWorkspace(withFallback(acme, true), "", true)
	if apperr.KindOf(err) != apperr.KindInvalid || !strings.Contains(err.Error(), stage.FallbackWorkspaceID) {
		t.Errorf("fallback with data: err = %v, want the ambiguity naming %q", err, stage.FallbackWorkspaceID)
	}
	if got, err := pickWorkspace(withFallback(acme, true), stage.FallbackWorkspaceID, true); err != nil || got != stage.FallbackWorkspaceID {
		t.Errorf("fallback named: got %q, %v", got, err)
	}
	if len(acme) != 1 {
		t.Error("withFallback changed its input")
	}
}

func TestWorkspaceLabel(t *testing.T) {
	rows := []workspaceRow{{id: "bbbb000000000008", slug: "acme"}}
	if got := workspaceLabel(rows, "bbbb000000000008"); got != "acme" {
		t.Errorf("label = %q, want the slug", got)
	}
	if got := workspaceLabel(rows, stage.FallbackWorkspaceID); got != stage.FallbackWorkspaceID {
		t.Errorf("label = %q, want the id of a workspace with no row", got)
	}
}

func TestSecretLine(t *testing.T) {
	got := secretLine(exportbundle.SecretItem{Kind: "credential", Name: "GitHub de Acme", Scope: "github/github_pat"})
	if strings.Contains(got, "()") || strings.Contains(got, "  ") {
		t.Errorf("line %q keeps the empty identity", got)
	}
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "GitHub de Acme github/github_pat") {
		t.Errorf("line = %q", got)
	}
	full := secretLine(exportbundle.SecretItem{Kind: "ingest_token", Name: "ci", Identity: "oct_ab12", Scope: "environment:prod"})
	if !strings.Contains(full, "ci (oct_ab12) environment:prod") {
		t.Errorf("line = %q", full)
	}
	if strings.Contains(full, "ingest_token") {
		t.Errorf("line %q shows the raw kind", full)
	}
	if got := secretLine(exportbundle.SecretItem{Kind: "future_kind", Name: "x"}); got != "[future_kind] x" {
		t.Errorf("unknown kind: %q", got)
	}
}

func TestPrintWorkspacesSaysWhenThereAreNone(t *testing.T) {
	var out, errOut bytes.Buffer
	printWorkspaces(&out, &errOut, nil)
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing a script would read as a row", out.String())
	}
	if !strings.Contains(errOut.String(), "workspace create") {
		t.Errorf("stderr = %q, want how to create one", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	printWorkspaces(&out, &errOut, []namedWorkspace{{id: "bbbb000000000008", name: "Acme", slug: "acme"}})
	if !strings.HasPrefix(out.String(), "bbbb000000000008  Acme") || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errOut.String())
	}
}

func TestDaemonUnreachableNamesTheAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	_ = ln.Close()
	t.Setenv("OCTOPUS_URL", url)

	conn, dialErr := net.Dial("tcp", ln.Addr().String())
	if dialErr == nil {
		_ = conn.Close()
		t.Skip("something took the port back")
	}
	got := daemonUnreachable(dialErr)
	text := got.Error()
	if apperr.KindOf(got) != apperr.KindUnavailable {
		t.Errorf("kind = %d", apperr.KindOf(got))
	}
	if !strings.Contains(text, url) || strings.Contains(text, "dial tcp") || strings.Contains(text, "connection refused") {
		t.Errorf("message = %q", text)
	}
}
