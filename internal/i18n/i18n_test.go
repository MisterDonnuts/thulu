package i18n

import (
	"context"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load(fstest.MapFS{
		"locales/en/demo.json": {Data: []byte(`{
			"hello": "Hello, {name}",
			"cost": "Cost {usd:%.2f}",
			"tasks": {"one": "{count} task", "other": "{count} tasks"},
			"group": {"nested": "deep"},
			"only_en": "only English"
		}`)},
		"locales/es/demo.json": {Data: []byte(`{
			"hello": "Hola, {name}",
			"cost": "Costo {usd:%.2f}",
			"tasks": {"one": "{count} tarea", "other": "{count} tareas"},
			"group": {"nested": "profundo"}
		}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRender(t *testing.T) {
	c := testCatalog(t)
	cases := []struct {
		locale, key string
		args        []any
		want        string
	}{
		{"en", "demo.hello", []any{"name", "Ana"}, "Hello, Ana"},
		{"es", "demo.hello", []any{"name", "Ana"}, "Hola, Ana"},
		{"es", "demo.cost", []any{"usd", 1.5}, "Costo 1.50"},
		{"en", "demo.tasks", []any{"count", 1}, "1 task"},
		{"en", "demo.tasks", []any{"count", 3}, "3 tasks"},
		{"es", "demo.tasks", []any{"count", int64(1)}, "1 tarea"},
		{"es", "demo.group.nested", nil, "profundo"},
		{"es", "demo.only_en", nil, "only English"},
		{"fr", "demo.hello", []any{"name", "A"}, "Hello, A"},
		{"en", "demo.hello", nil, "Hello, {name}"},
		{"en", "demo.nope", nil, "demo.nope"},
	}
	for _, tc := range cases {
		if got := c.Render(tc.locale, tc.key, tc.args...); got != tc.want {
			t.Errorf("Render(%q, %q, %v) = %q, want %q", tc.locale, tc.key, tc.args, got, tc.want)
		}
	}
}

func TestPluralNeedsOther(t *testing.T) {
	c, err := Load(fstest.MapFS{
		"locales/en/x.json": {Data: []byte(`{"g": {"one": "a group, not a plural"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Has("en", "x.g.one") {
		t.Fatal("an object without \"other\" is a group of keys")
	}
}

func TestLoadRejectsNonString(t *testing.T) {
	_, err := Load(fstest.MapFS{"locales/en/x.json": {Data: []byte(`{"n": 1}`)}})
	if err == nil {
		t.Fatal("a number is not a message")
	}
}

func TestNegotiate(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"es-AR,es;q=0.9,en;q=0.8": "es",
		"fr-FR, en;q=0.5":         "en",
		"en;q=0.2, es;q=0.9":      "es",
		"de, fr":                  "",
		"ES":                      "es",
		"es;q=0":                  "",
	}
	for header, want := range cases {
		if got := Negotiate(header); got != want {
			t.Errorf("Negotiate(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestFromRequestPrefersCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en")
	r.Header.Set("Cookie", LocaleCookie+"=es")
	if got := FromRequest(r); got != "es" {
		t.Fatalf("cookie should win, got %q", got)
	}
	r.Header.Set("Cookie", LocaleCookie+"=xx")
	if got := FromRequest(r); got != "en" {
		t.Fatalf("an unknown cookie falls through to the header, got %q", got)
	}
}

func TestContextLocale(t *testing.T) {
	ctx := WithLocale(context.Background(), "es")
	if FromContext(ctx) != "es" {
		t.Fatal("context locale lost")
	}
	if FromContext(context.Background()) != DaemonLocale() {
		t.Fatal("a bare context takes the daemon's locale")
	}
}

func TestSetDaemonLocale(t *testing.T) {
	t.Cleanup(func() { SetDaemonLocale("") })
	if !SetDaemonLocale("es-AR") || DaemonLocale() != "es" {
		t.Fatalf("es-AR should select es, got %q", DaemonLocale())
	}
	if SetDaemonLocale("xx") || DaemonLocale() != SourceLocale {
		t.Fatalf("an unknown locale falls back to the source, got %q", DaemonLocale())
	}
}

func TestPlaceholders(t *testing.T) {
	got := Placeholders("{a} and {b:%d} and {a} but not { c } or {}")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("Placeholders = %v", got)
	}
}
