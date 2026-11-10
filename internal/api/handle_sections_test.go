package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestIntegration_TaskSections(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sections@test.com", "S", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "sectioned task", "body": "Free-form intro.", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	gw := putJSON(t, h, "/tasks/"+id+"/sections/goal", map[string]any{
		"content": "Export the dashboard to PDF.", "author": "refiner",
	}, token)
	if gw.Code != http.StatusOK {
		t.Fatalf("PUT goal = %d: %s", gw.Code, gw.Body.String())
	}
	dw := putJSON(t, h, "/tasks/"+id+"/sections/definition-of-done", map[string]any{
		"content": "- [ ] button visible\n- [ ] PDF has all columns", "author": "refiner",
	}, token)
	if dw.Code != http.StatusOK {
		t.Fatalf("PUT dod = %d: %s", dw.Code, dw.Body.String())
	}

	sw := getJSON(t, h, "/tasks/"+id+"/sections", token)
	if sw.Code != http.StatusOK {
		t.Fatalf("GET sections = %d", sw.Code)
	}
	var view struct {
		Preamble string `json:"preamble"`
		Sections []struct {
			Slug  string `json:"slug"`
			Title string `json:"title"`
			Items []struct {
				Text    string `json:"text"`
				Checked bool   `json:"checked"`
			} `json:"items"`
		} `json:"sections"`
	}
	json.NewDecoder(sw.Body).Decode(&view)
	if !strings.Contains(view.Preamble, "Free-form intro") {
		t.Errorf("preamble lost: %q", view.Preamble)
	}
	if len(view.Sections) != 2 || view.Sections[0].Slug != "goal" ||
		view.Sections[1].Slug != "definition-of-done" {
		t.Fatalf("sections = %+v", view.Sections)
	}
	if len(view.Sections[1].Items) != 2 || view.Sections[1].Items[0].Checked {
		t.Fatalf("dod items = %+v", view.Sections[1].Items)
	}
	if view.Sections[1].Title != "Definition of Done" {
		t.Errorf("canonical title not applied: %q", view.Sections[1].Title)
	}

	pw := patchJSONAuth(t, h, "/tasks/"+id+"/sections/definition-of-done/items/2", token,
		map[string]any{"checked": true, "note": "verified in TestExport", "author": "tester"})
	if pw.Code != http.StatusOK {
		t.Fatalf("PATCH item = %d: %s", pw.Code, pw.Body.String())
	}
	sw2 := getJSON(t, h, "/tasks/"+id+"/sections", token)
	json.NewDecoder(sw2.Body).Decode(&view)
	it := view.Sections[1].Items[1]
	if !it.Checked || !strings.Contains(it.Text, "verified in TestExport") {
		t.Errorf("checked item = %+v", it)
	}

	tw := getJSON(t, h, "/tasks/"+id, token)
	var full map[string]any
	json.NewDecoder(tw.Body).Decode(&full)
	body := full["body"].(string)
	if !strings.Contains(body, "- [x] PDF has all columns — verified in TestExport") {
		t.Errorf("body out of sync:\n%s", body)
	}

	if w := patchJSONAuth(t, h, "/tasks/"+id+"/sections/definition-of-done/items/9", token,
		map[string]any{"checked": true}); w.Code != http.StatusBadRequest {
		t.Errorf("out-of-range index = %d, want 400", w.Code)
	}
	if w := patchJSONAuth(t, h, "/tasks/"+id+"/sections/nope/items/1", token,
		map[string]any{"checked": true}); w.Code != http.StatusNotFound {
		t.Errorf("missing section = %d, want 404", w.Code)
	}
	if w := patchJSONAuth(t, h, "/tasks/"+id+"/sections/definition-of-done/items/1", token,
		map[string]any{"note": "no checked"}); w.Code != http.StatusBadRequest {
		t.Errorf("missing checked = %d, want 400", w.Code)
	}
	if w := putJSON(t, h, "/tasks/"+id+"/sections/my-own", map[string]any{
		"content": "x",
	}, token); w.Code != http.StatusBadRequest {
		t.Errorf("custom w/o title = %d, want 400", w.Code)
	}
}
