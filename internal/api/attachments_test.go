package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func uploadBase64(t *testing.T, h http.Handler, token, taskID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return postJSONAuth(t, h, "/tasks/"+taskID+"/attachments", token, body)
}

func decodeAttachment(t *testing.T, w *httptest.ResponseRecorder) task.Attachment {
	t.Helper()
	var a task.Attachment
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode attachment: %v (body %s)", err, w.Body.String())
	}
	return a
}

func TestIntegration_UploadAttachmentJSON(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "att-json@test.com", "AJ", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "json upload")

	content := []byte("hello bytes")
	w := uploadBase64(t, h, token, taskID, map[string]any{
		"filename":       "notes.txt",
		"mime_type":      "text/plain",
		"content_base64": base64.StdEncoding.EncodeToString(content),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("POST attachments (json) = %d, want 200: %s", w.Code, w.Body.String())
	}
	a := decodeAttachment(t, w)
	if a.Size != int64(len(content)) {
		t.Errorf("size = %d, want %d", a.Size, len(content))
	}
	if a.Author == "" {
		t.Error("author empty; want the session's identity")
	}

	stored, err := srv.taskStore.GetAttachment(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	onDisk, err := os.ReadFile(stored.Path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(onDisk) != string(content) {
		t.Errorf("stored %q, want %q", onDisk, content)
	}
}

func TestIntegration_UploadAttachmentJSON_BadBase64(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "att-bad64@test.com", "AB", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "bad base64")

	w := uploadBase64(t, h, token, taskID, map[string]any{
		"filename":       "x.bin",
		"content_base64": "not base64!!",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid base64 = %d, want 400", w.Code)
	}
}

func TestIntegration_UploadAttachment_SameNameAndSize(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "att-collide@test.com", "AC", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "collision")

	first := decodeAttachment(t, uploadBase64(t, h, token, taskID, map[string]any{
		"filename":       "shot.png",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("AAAA")),
	}))
	second := decodeAttachment(t, uploadBase64(t, h, token, taskID, map[string]any{
		"filename":       "shot.png",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("BBBB")),
	}))

	a1, err := srv.taskStore.GetAttachment(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	a2, err := srv.taskStore.GetAttachment(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if a1.Path == a2.Path {
		t.Fatalf("both uploads share the path %s", a1.Path)
	}
	b1, _ := os.ReadFile(a1.Path)
	if string(b1) != "AAAA" {
		t.Errorf("first file = %q, want AAAA — the second upload overwrote it", b1)
	}
}

func TestIntegration_UploadAttachment_RejectsForeignComment(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "att-foreign@test.com", "AF", "SecurePassword1")
	taskA := createTaskViaAPI(t, h, token, "task a")
	taskB := createTaskViaAPI(t, h, token, "task b")

	w := postJSONAuth(t, h, "/tasks/"+taskB+"/comments", token, map[string]any{"body": "on b"})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST comment = %d: %s", w.Code, w.Body.String())
	}
	var c task.Comment
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode comment: %v", err)
	}

	got := uploadBase64(t, h, token, taskA, map[string]any{
		"filename":       "x.txt",
		"comment_id":     c.ID,
		"content_base64": base64.StdEncoding.EncodeToString([]byte("x")),
	})
	if got.Code != http.StatusBadRequest {
		t.Errorf("upload with another task's comment_id = %d, want 400", got.Code)
	}
}

func TestIntegration_CommentBindsAttachments(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "att-bind@test.com", "AB2", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "bind on post")

	att := decodeAttachment(t, uploadBase64(t, h, token, taskID, map[string]any{
		"filename":       "evidence.png",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("PNG")),
	}))
	if att.CommentID != "" {
		t.Fatalf("fresh upload already bound to comment %s", att.CommentID)
	}

	w := postJSONAuth(t, h, "/tasks/"+taskID+"/comments", token, map[string]any{
		"body":           "here it is",
		"attachment_ids": []string{att.ID},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST comment = %d: %s", w.Code, w.Body.String())
	}
	var c task.Comment
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode comment: %v", err)
	}

	list := getJSON(t, h, "/tasks/"+taskID+"/attachments", token)
	if list.Code != http.StatusOK {
		t.Fatalf("GET attachments = %d: %s", list.Code, list.Body.String())
	}
	var atts []task.Attachment
	if err := json.Unmarshal(list.Body.Bytes(), &atts); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(atts))
	}
	if atts[0].CommentID != c.ID {
		t.Errorf("comment_id = %q, want %q", atts[0].CommentID, c.ID)
	}
}

func TestIntegration_CommentWithOnlyAttachments(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "att-onlyfiles@test.com", "AO", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "files only")

	att := decodeAttachment(t, uploadBase64(t, h, token, taskID, map[string]any{
		"filename":       "clip.mp4",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("MP4")),
	}))
	w := postJSONAuth(t, h, "/tasks/"+taskID+"/comments", token, map[string]any{
		"attachment_ids": []string{att.ID},
	})
	if w.Code != http.StatusCreated {
		t.Errorf("POST comment with no body but one file = %d, want 201: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_DeleteAttachment(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "att-del@test.com", "AD", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "delete me")

	att := decodeAttachment(t, uploadBase64(t, h, token, taskID, map[string]any{
		"filename":       "gone.txt",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("bye")),
	}))
	stored, err := srv.taskStore.GetAttachment(t.Context(), att.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}

	r := httptest.NewRequest(http.MethodDelete, "/task-attachments/"+att.ID, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204: %s", w.Code, w.Body.String())
	}
	if _, err := srv.taskStore.GetAttachment(t.Context(), att.ID); err == nil {
		t.Error("row still readable after delete")
	}
	if _, err := os.Stat(stored.Path); !os.IsNotExist(err) {
		t.Errorf("file still on disk at %s", stored.Path)
	}
}

func TestIntegration_ServeAttachment_FramingHeaders(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "att-frame@test.com", "AFR", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "framing")

	att := decodeAttachment(t, uploadBase64(t, h, token, taskID, map[string]any{
		"filename":       "report.pdf",
		"mime_type":      "application/pdf",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("%PDF-1.4")),
	}))
	w := getJSON(t, h, "/task-attachments/"+att.ID, token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("CSP = %q, want frame-ancestors 'self'", csp)
	}
	if !strings.Contains(csp, "sandbox allow-scripts") {
		t.Errorf("CSP = %q, want the sandbox directive — an uploaded HTML file must not run on this origin", csp)
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes — a video seeks by range", got)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "private") || strings.Contains(cc, "public") {
		t.Errorf("Cache-Control = %q, want private", cc)
	}
}
