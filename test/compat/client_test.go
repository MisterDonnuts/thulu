//go:build compat

package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const requestTimeout = 20 * time.Second

type client struct {
	t    *testing.T
	base string
	http *http.Client
	key  string
	csrf string
}

func env(t *testing.T, name string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		t.Fatalf("%s is not set: run the gate through scripts/compat.sh", name)
	}
	return v
}

func devClient(t *testing.T) *client {
	t.Helper()
	base := strings.TrimRight(env(t, "COMPAT_URL"), "/")
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, base: base, http: &http.Client{Timeout: requestTimeout, Jar: jar}}
	resp, err := c.http.Get(base + "/?token=" + url.QueryEscape(env(t, "COMPAT_DEV_TOKEN")))
	if err != nil {
		t.Fatalf("open dev session: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	u, _ := url.Parse(base)
	for _, ck := range jar.Cookies(u) {
		if ck.Name == "octopus_csrf" {
			c.csrf = ck.Value
		}
	}
	if c.csrf == "" && !openDevMode(c) {
		t.Fatalf("dev session: no CSRF cookie after GET /?token= (status %d); is OCTOPUS_DEV_MODE on?", resp.StatusCode)
	}
	return c
}

// Releases up to 0.1.0 skip auth entirely in dev mode, so no CSRF cookie exists. Probing an
// admin-only route keeps a daemon that lost dev mode failing.
func openDevMode(c *client) bool {
	req, err := http.NewRequest(http.MethodGet, c.base+"/audit/verify", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func keyClient(t *testing.T, key string) *client {
	t.Helper()
	return &client{
		t:    t,
		base: strings.TrimRight(env(t, "COMPAT_URL"), "/"),
		http: &http.Client{Timeout: requestTimeout},
		key:  key,
	}
}

func (c *client) do(method, path string, body, out any) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	} else if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		c.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	if resp.StatusCode >= 500 {
		c.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, snippet(raw))
	}
	if out != nil && resp.StatusCode < 300 && len(raw) > 0 {
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "<") {
			c.t.Fatalf("%s %s: got HTML, not JSON (route missing in this version?)", method, path)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decode %s: %v", method, path, snippet(raw), err)
		}
	}
	return resp.StatusCode, raw
}

func (c *client) must(method, path string, body, out any) {
	c.t.Helper()
	if code, raw := c.do(method, path, body, out); code < 200 || code >= 300 {
		c.t.Fatalf("%s %s: status %d %s", method, path, code, snippet(raw))
	}
}

func (c *client) optional(method, path string, body, out any) bool {
	c.t.Helper()
	code, raw := c.do(method, path, body, nil)
	trimmed := strings.TrimSpace(string(raw))
	if (code == http.StatusNotFound && strings.Contains(trimmed, "no such endpoint")) ||
		(code < 300 && strings.HasPrefix(trimmed, "<")) {
		return false
	}
	if code < 200 || code >= 300 {
		c.t.Fatalf("%s %s: status %d %s", method, path, code, snippet(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decode %s: %v", method, path, snippet(raw), err)
		}
	}
	return true
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// Versions return lists either bare or wrapped under key; accept both.
func items(v any, key string) []map[string]any {
	if m, ok := v.(map[string]any); ok {
		v = m[key]
	}
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func settingValue(v any) string {
	if m, ok := v.(map[string]any); ok {
		v = m["value"]
	}
	return fmt.Sprint(v)
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}
