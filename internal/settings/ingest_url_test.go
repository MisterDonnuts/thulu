package settings

import (
	"strings"
	"testing"
)

func TestIngestPublicURLIsEmptyUntilSomebodySetsIt(t *testing.T) {
	s := newTestStore(t)
	got, err := s.IngestPublicURL(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("IngestPublicURL = %q on a fresh store, want empty", got)
	}
}

func TestSetIngestPublicURLRoundTrips(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetIngestPublicURL(t.Context(), "https://thulu.example.com"); err != nil {
		t.Fatal(err)
	}
	got, err := s.IngestPublicURL(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://thulu.example.com" {
		t.Errorf("IngestPublicURL = %q", got)
	}
}

func TestSetIngestPublicURLAcceptsClearing(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetIngestPublicURL(t.Context(), "https://thulu.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIngestPublicURL(t.Context(), "  "); err != nil {
		t.Fatalf("clearing the URL: %v", err)
	}
	got, err := s.IngestPublicURL(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("IngestPublicURL = %q after clearing, want empty", got)
	}
}

func TestNormalizeIngestPublicURLRejectsAddressesThatWouldFailSilently(t *testing.T) {
	cases := map[string]string{
		"no scheme":       "10.0.0.5:8585",
		"bare host":       "thulu.example.com",
		"wrong scheme":    "ftp://thulu.example.com",
		"carries a query": "https://thulu.example.com?token=abc",
		"has a fragment":  "https://thulu.example.com#ingest",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeIngestPublicURL(raw); err == nil {
				t.Errorf("NormalizeIngestPublicURL(%q) was accepted; it must not be", raw)
			}
		})
	}
}

func TestNormalizeIngestPublicURLNamesTheSchemeInEveryRefusal(t *testing.T) {
	for _, raw := range []string{"10.0.0.5:8585", "localhost:8585", "thulu.example.com"} {
		t.Run(raw, func(t *testing.T) {
			_, err := NormalizeIngestPublicURL(raw)
			if err == nil {
				t.Fatalf("%q has no scheme and must be refused", raw)
			}
			if !strings.Contains(err.Error(), "http://") {
				t.Errorf("the refusal must say what a good address looks like, got %q", err)
			}
		})
	}
}

func TestNormalizeIngestPublicURLTrimsTheTrailingSlash(t *testing.T) {
	got, err := NormalizeIngestPublicURL("https://thulu.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://thulu.example.com" {
		t.Errorf("NormalizeIngestPublicURL = %q, want the slash trimmed", got)
	}
}

func TestNormalizeIngestPublicURLKeepsAPathPrefix(t *testing.T) {
	got, err := NormalizeIngestPublicURL("https://example.com/octopus/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/octopus" {
		t.Errorf("NormalizeIngestPublicURL = %q, want the prefix kept", got)
	}
}
