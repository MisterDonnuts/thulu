package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestOpenAPISpec_EveryTextRenders(t *testing.T) {
	for _, loc := range []openAPILocale{"en", "es"} {
		raw, err := json.Marshal(buildOpenAPISpec(loc))
		if err != nil {
			t.Fatalf("%s: marshal: %v", loc, err)
		}
		if strings.Contains(string(raw), "api.openapi.") {
			t.Errorf("%s: the spec carries an unrendered catalog key", loc)
		}
	}
}

func TestOpenAPISpec_AnswersInTheCallersLanguage(t *testing.T) {
	get := func(lang string) openAPISpec {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		r.Header.Set("Accept-Language", lang)
		w := httptest.NewRecorder()
		(&Server{}).handleOpenAPI(w, r)
		if !strings.Contains(w.Header().Get("Vary"), "Accept-Language") {
			t.Errorf("%s: Vary = %q, want it to name Accept-Language", lang, w.Header().Get("Vary"))
		}
		var spec openAPISpec
		if err := json.NewDecoder(w.Body).Decode(&spec); err != nil {
			t.Fatalf("%s: decode: %v", lang, err)
		}
		return spec
	}
	en, es := get("en"), get("es")
	if got := en.Paths["/tasks"]["get"].Summary; got != "List all tasks" {
		t.Errorf("en summary = %q, want %q", got, "List all tasks")
	}
	if got := es.Paths["/tasks"]["get"].Summary; got != "Listar todas las tareas" {
		t.Errorf("es summary = %q, want %q", got, "Listar todas las tareas")
	}
	if _, ok := es.Components.Schemas["Task"]; !ok {
		t.Error("es spec lost the Task schema")
	}
}

func TestOpenAPISpec_DeclaresEveryPathParameter(t *testing.T) {
	placeholder := regexp.MustCompile(`\{([^}]+)\}`)
	spec := buildOpenAPISpec("en")
	for path, ops := range spec.Paths {
		for method, op := range ops {
			declared := map[string]bool{}
			for _, p := range op.Parameters {
				if p.In == "path" {
					declared[p.Name] = true
				}
			}
			for _, m := range placeholder.FindAllStringSubmatch(path, -1) {
				if !declared[m[1]] {
					t.Errorf("%s %s: path parameter %q is not declared", method, path, m[1])
				}
			}
		}
	}
}

func TestOpenAPISpec_DeliveriesAreAList(t *testing.T) {
	op := buildOpenAPISpec("en").Paths["/webhooks/{id}/deliveries"]["get"]
	schema := op.Responses["200"].Content["application/json"].Schema
	if schema.Type != "array" || schema.Items == nil || schema.Items.Ref != "#/components/schemas/"+schemaWebhookDelivery {
		t.Errorf("deliveries response = %+v, want an array of %s", schema, schemaWebhookDelivery)
	}
}
