package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func uploadTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func postUpload(t *testing.T, h http.Handler, path, token string, meta any, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if meta != nil {
		raw, _ := json.Marshal(meta)
		if err := mw.WriteField("meta", string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if archive != nil {
		fw, err := mw.CreateFormFile("archive", "folder.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(archive); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIntegration_ProjectUploadFlow(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "uploader@example.com", "Uploader", "SecurePassword1")

	w := postUpload(t, h, "/projects/upload", token, map[string]any{"name": "Folder App"}, uploadTarGz(t, map[string]string{
		"README.md":           "# folder\n",
		".env":                "TOKEN=x\n",
		"node_modules/a/i.js": "x\n",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /projects/upload = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Project struct {
			ID            string `json:"id"`
			Source        string `json:"source"`
			DefaultBranch string `json:"default_branch"`
		} `json:"project"`
		Report struct {
			Files    int      `json:"files"`
			Excluded []string `json:"excluded"`
			Skipped  []string `json:"skipped"`
		} `json:"report"`
	}
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	id := created.Project.ID
	if created.Project.Source != "upload" || created.Project.DefaultBranch != "main" || created.Report.Files != 1 ||
		!slices.Contains(created.Report.Excluded, ".env") || !slices.Contains(created.Report.Skipped, "node_modules/") {
		t.Errorf("created = %+v", created)
	}

	w = getJSON(t, h, "/projects/"+id+"/branches", token)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"branches":["main"]`) ||
		!strings.Contains(w.Body.String(), `"task_branches":[]`) {
		t.Errorf("GET branches = %d %s", w.Code, w.Body.String())
	}

	w = getJSON(t, h, "/projects/"+id+"/archive", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET archive = %d %s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="Folder_App-main.zip"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	var entries []string
	var base string
	if err == nil {
		for _, f := range zr.File {
			entries = append(entries, f.Name)
			if f.Name == ".octopus-base" {
				rc, _ := f.Open()
				raw, _ := io.ReadAll(rc)
				_ = rc.Close()
				base = strings.TrimSpace(string(raw))
			}
		}
	}
	if err != nil || !slices.Equal(entries, []string{"README.md", ".octopus-base"}) || len(base) != 40 {
		t.Errorf("archive: %v %v base %q", err, entries, base)
	}

	w = postUpload(t, h, "/projects/"+id+"/upload", token, nil, uploadTarGz(t, map[string]string{"README.md": "# v2\n"}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":true`) {
		t.Errorf("POST re-upload = %d %s", w.Code, w.Body.String())
	}

	stale := uploadTarGz(t, map[string]string{".octopus-base": base + "\n", "README.md": "# folder\n", "notes.txt": "mine\n"})
	w = postUpload(t, h, "/projects/"+id+"/upload", token, nil, stale)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"api.projects.errors.main_moved"`) ||
		!strings.Contains(w.Body.String(), `"ahead":1`) {
		t.Errorf("POST stale re-upload = %d %s, want 409 main_moved ahead 1", w.Code, w.Body.String())
	}
	w = postUpload(t, h, "/projects/"+id+"/upload?mode=merge", token, nil, stale)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":true`) {
		t.Errorf("POST stale re-upload with mode=merge = %d %s", w.Code, w.Body.String())
	}
	if w := postUpload(t, h, "/projects/"+id+"/upload?mode=nope", token, nil, stale); w.Code != http.StatusBadRequest {
		t.Errorf("POST re-upload with an unknown mode = %d", w.Code)
	}

	if w := postUpload(t, h, "/projects/upload", token, nil, uploadTarGz(t, map[string]string{"a": "1"})); w.Code != http.StatusBadRequest {
		t.Errorf("upload without meta = %d", w.Code)
	}
	if w := postUpload(t, h, "/projects/upload", token, map[string]any{"name": "No Archive"}, nil); w.Code != http.StatusBadRequest {
		t.Errorf("upload without archive = %d", w.Code)
	}
	if w := postUpload(t, h, "/projects/upload", token, map[string]any{"name": "Not An Archive"}, []byte("plain text")); w.Code != http.StatusBadRequest {
		t.Errorf("upload of a non-archive = %d %s", w.Code, w.Body.String())
	}
}

func TestPersonOnlyWriteCoversMergeAndUpload(t *testing.T) {
	for _, route := range []string{"/tasks/{id}/merge", "/projects/upload", "/projects/{id}/upload"} {
		if got := personOnlyWrite(route); got != "api.scope.errors.run_merge" {
			t.Errorf("personOnlyWrite(%s) = %q", route, got)
		}
	}
}

func TestWriteMergeConflictSaysWhetherTheAgentCanResolve(t *testing.T) {
	for _, can := range []bool{true, false} {
		rec := httptest.NewRecorder()
		writeMergeConflict(rec, "clash", []string{"a.txt"}, can)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
		var body struct {
			Conflicts       []string `json:"conflicts"`
			AgentCanResolve bool     `json:"agent_can_resolve"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.AgentCanResolve != can || !slices.Equal(body.Conflicts, []string{"a.txt"}) {
			t.Errorf("agentCanResolve %v: body = %+v", can, body)
		}
	}
}
