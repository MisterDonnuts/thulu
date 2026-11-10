package i18n

import (
	"strings"
	"testing"
	"testing/fstest"
)

func retranslateCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load(fstest.MapFS{
		"locales/en/demo.json": {Data: []byte(`{
			"audit": {
				"key_created": "API key {name} created with role {role}",
				"purged": {"one": "Retention removed {count} entry", "other": "Retention removed {count} entries"},
				"quoted": "Setting {key:%q} changed",
				"loose": "{reason} ({id})",
				"money": "Spent {usd:%.2f} dollars today"
			},
			"other": {"key_created": "API key {name} created with role {role}"},
			"audit_legacy": {"key_created": "Created API key {name} ({role})"}
		}`)},
		"locales/es/demo.json": {Data: []byte(`{
			"audit": {
				"key_created": "Se creó la clave de API {name} con rol {role}",
				"purged": {"one": "La retención borró {count} entrada", "other": "La retención borró {count} entradas"},
				"quoted": "Se cambió la configuración {key:%q}",
				"loose": "{reason} ({id})",
				"money": "Se gastaron {usd:%.2f} dólares hoy"
			},
			"other": {"key_created": "Se creó la clave de API {name} con rol {role}"},
			"audit_legacy": {"key_created": "Clave de API creada: {name} ({role})"}
		}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRetranslator(t *testing.T) {
	rt := NewRetranslator(retranslateCatalog(t), func(key string) bool {
		return strings.HasPrefix(key, "demo.audit.")
	})
	cases := []struct {
		name, text, locale, want string
		ok                       bool
	}{
		{"english to spanish", "API key CI created with role viewer", "es",
			"Se creó la clave de API CI con rol viewer", true},
		{"spanish to english", "Se creó la clave de API Nightly export con rol admin", "en",
			"API key Nightly export created with role admin", true},
		{"same locale stays verbatim", "API key CI created with role viewer", "en",
			"API key CI created with role viewer", true},
		{"plural keeps its form", "Retention removed 3 entries", "es", "La retención borró 3 entradas", true},
		{"plural singular", "Retention removed 1 entry", "es", "La retención borró 1 entrada", true},
		{"quoted value round-trips", `Setting "audit.retention_days" changed`, "es",
			`Se cambió la configuración "audit.retention_days"`, true},
		{"unknown text is left alone", "Something nobody wrote a message for", "es",
			"Something nobody wrote a message for", false},
		{"template with too little fixed text is not matched", "boom (row 4)", "es", "boom (row 4)", false},
		{"float verbs are not read back", "Spent 3.50 dollars today", "es", "Spent 3.50 dollars today", false},
		{"empty", "", "es", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := rt.Translate(c.text, c.locale)
			if got != c.want || ok != c.ok {
				t.Errorf("Translate(%q, %s) = %q, %v; want %q, %v", c.text, c.locale, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestRetranslatorNil(t *testing.T) {
	var rt *Retranslator
	if got, ok := rt.Translate("x", "es"); got != "x" || ok {
		t.Errorf("nil Retranslator changed the text: %q %v", got, ok)
	}
}

func TestRetranslatorValues(t *testing.T) {
	roles := map[string]map[string]string{
		"es": {"viewer": "lector"},
		"en": {"viewer": "viewer"},
	}
	base := NewRetranslator(retranslateCatalog(t), func(key string) bool {
		return strings.HasPrefix(key, "demo.audit.")
	})
	rt := base.WithValues(func(locale, _ string, vals map[string]any) {
		role, _ := vals["role"].(string)
		if label, ok := roles[locale][role]; ok {
			vals["role"] = label
		}
	})
	if got, _ := base.Translate("API key CI created with role viewer", "en"); got != "API key CI created with role viewer" {
		t.Errorf("WithValues changed the Retranslator it copied: %q", got)
	}
	cases := []struct{ text, locale, want string }{
		{"Se creó la clave de API CI con rol viewer", "es", "Se creó la clave de API CI con rol lector"},
		{"API key CI created with role viewer", "es", "Se creó la clave de API CI con rol lector"},
		{"Se creó la clave de API CI con rol viewer", "en", "API key CI created with role viewer"},
		{"Se creó la clave de API CI con rol robot", "es", "Se creó la clave de API CI con rol robot"},
		{"Retention removed 3 entries", "en", "Retention removed 3 entries"},
	}
	for _, c := range cases {
		if got, ok := rt.Translate(c.text, c.locale); got != c.want || !ok {
			t.Errorf("Translate(%q, %s) = %q, %v; want %q", c.text, c.locale, got, ok, c.want)
		}
	}
}

func TestRetranslatorLegacy(t *testing.T) {
	rt := NewRetranslator(retranslateCatalog(t), func(key string) bool {
		return strings.HasPrefix(key, "demo.audit.")
	}).WithLegacy(map[string]string{"demo.audit_legacy.key_created": "demo.audit.key_created"})
	cases := []struct{ text, locale, want string }{
		{"Created API key CI (viewer)", "es", "Se creó la clave de API CI con rol viewer"},
		{"Created API key CI (viewer)", "en", "API key CI created with role viewer"},
		{"Clave de API creada: CI (admin)", "en", "API key CI created with role admin"},
		{"API key CI created with role viewer", "en", "API key CI created with role viewer"},
	}
	for _, c := range cases {
		if got, ok := rt.Translate(c.text, c.locale); got != c.want || !ok {
			t.Errorf("Translate(%q, %s) = %q, %v; want %q", c.text, c.locale, got, ok, c.want)
		}
	}
}
