package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLLMKey_SetStatusDelete(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	const secret = "sk-ant-api-super-secret-7777"

	body, _ := json.Marshal(map[string]string{"provider": "anthropic", "key": secret})
	put := httptest.NewRequest(http.MethodPut, "/llm-key", strings.NewReader(string(body)))
	put.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, put)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /llm-key = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("PUT response leaked the raw key")
	}

	g := httptest.NewRequest(http.MethodGet, "/llm-key", nil)
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, g)
	if gw.Code != http.StatusOK {
		t.Fatalf("GET /llm-key = %d", gw.Code)
	}
	gotBody := gw.Body.String()
	if strings.Contains(gotBody, secret) {
		t.Fatal("GET status leaked the raw key")
	}
	var st map[string]any
	if err := json.Unmarshal(gw.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["has_key"] != true {
		t.Errorf("expected has_key=true, got %v", st["has_key"])
	}
	if hint, _ := st["hint"].(string); !strings.HasPrefix(hint, "…") {
		t.Errorf("expected masked hint, got %q", hint)
	}

	d := httptest.NewRequest(http.MethodDelete, "/llm-key", nil)
	dw := httptest.NewRecorder()
	h.ServeHTTP(dw, d)
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE /llm-key = %d", dw.Code)
	}
	g2 := httptest.NewRequest(http.MethodGet, "/llm-key", nil)
	g2w := httptest.NewRecorder()
	h.ServeHTTP(g2w, g2)
	var st2 map[string]any
	_ = json.Unmarshal(g2w.Body.Bytes(), &st2)
	if st2["has_key"] != false {
		t.Errorf("expected has_key=false after delete, got %v", st2["has_key"])
	}
}

func TestEngines_ListAndPerEngineKey(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	badBody, _ := json.Marshal(map[string]string{"engine": "claude-code", "key": "nope-123"})
	bad := httptest.NewRequest(http.MethodPut, "/llm-key", strings.NewReader(string(badBody)))
	bad.Header.Set("Content-Type", "application/json")
	bw := httptest.NewRecorder()
	h.ServeHTTP(bw, bad)
	if bw.Code != http.StatusBadRequest {
		t.Fatalf("wrong-prefix key: expected 400, got %d body=%s", bw.Code, bw.Body.String())
	}

	okBody, _ := json.Marshal(map[string]string{"engine": "claude-code", "key": "sk-ant-api-engine-key-42"})
	put := httptest.NewRequest(http.MethodPut, "/llm-key", strings.NewReader(string(okBody)))
	put.Header.Set("Content-Type", "application/json")
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, put)
	if pw.Code != http.StatusOK {
		t.Fatalf("PUT engine key: expected 200, got %d body=%s", pw.Code, pw.Body.String())
	}

	g := httptest.NewRequest(http.MethodGet, "/engines", nil)
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, g)
	if gw.Code != http.StatusOK {
		t.Fatalf("GET /engines = %d", gw.Code)
	}
	if strings.Contains(gw.Body.String(), "sk-ant-api-engine-key-42") {
		t.Fatal("GET /engines leaked the raw key")
	}
	var resp struct {
		Engines []struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
			HasKey   bool   `json:"has_key"`
			KeyHint  string `json:"key_hint"`
			KeyKinds []struct {
				ID     string `json:"id"`
				Label  string `json:"label"`
				Prefix string `json:"prefix"`
			} `json:"key_kinds"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(gw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range resp.Engines {
		if e.Name == "claude-code" {
			found = true
			if e.Provider != "anthropic" || !e.HasKey || !strings.HasPrefix(e.KeyHint, "…") {
				t.Errorf("claude-code engine = %+v", e)
			}
			var sawAPI, sawOAuth bool
			for _, k := range e.KeyKinds {
				switch k.ID {
				case "api":
					sawAPI = k.Prefix == "sk-ant-api"
				case "oauth":
					sawOAuth = k.Prefix == "sk-ant-oat"
				}
			}
			if !sawAPI || !sawOAuth {
				t.Errorf("claude-code key_kinds missing api/oauth: %+v", e.KeyKinds)
			}
		}
	}
	if !found {
		t.Fatalf("claude-code not in engines list: %s", gw.Body.String())
	}
}

func TestLLMKey_ExplicitKindValidation(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	put := func(t *testing.T, body map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/llm-key", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	if w := put(t, map[string]string{
		"engine": "claude-code", "kind": "oauth", "key": "sk-ant-oat-from-keychain",
	}); w.Code != http.StatusOK {
		t.Fatalf("kind=oauth oauth-key: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	if w := put(t, map[string]string{
		"engine": "claude-code", "kind": "oauth", "key": "sk-ant-api-not-an-oauth",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("kind=oauth api-key: expected 400, got %d body=%s", w.Code, w.Body.String())
	}

	if w := put(t, map[string]string{
		"engine": "claude-code", "kind": "bogus", "key": "sk-ant-api-whatever",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind: expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}
