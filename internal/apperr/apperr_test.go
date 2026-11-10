package apperr_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var errNotFound = apperr.NotFound("task.errors.not_found")

func TestIsMatchesByKey(t *testing.T) {
	err := apperr.Wrap(apperr.NotFound("task.errors.not_found"), "auth.errors.internal")
	if !errors.Is(err, errNotFound) {
		t.Fatal("a copy of the sentinel deeper in the chain should match it")
	}
	if errors.Is(err, apperr.New("auth.errors.user_not_found")) {
		t.Fatal("a different key is a different error")
	}
}

func TestKindPropagates(t *testing.T) {
	err := apperr.Wrap(apperr.Wrap(errNotFound, "auth.errors.internal"), "auth.errors.internal")
	if apperr.Status(err) != http.StatusNotFound {
		t.Fatalf("Status = %d, want 404 from the wrapped NotFound", apperr.Status(err))
	}
	if apperr.Status(errors.New("plain")) != http.StatusInternalServerError {
		t.Fatal("a foreign error is internal")
	}
	k := apperr.WrapKind(apperr.KindConflict, errNotFound, "auth.errors.internal")
	if apperr.Status(k) != http.StatusConflict {
		t.Fatal("an explicit kind wins over the cause's")
	}
	if apperr.Wrap(nil, "auth.errors.internal") != nil {
		t.Fatal("wrapping nil is nil")
	}
}

func TestLocalizeChain(t *testing.T) {
	err := apperr.Wrap(fs.ErrNotExist, "auth.errors.internal")
	if got, want := apperr.Localize(err, "es"), i18n.T("es", "auth.errors.internal")+": file does not exist"; got != want {
		t.Fatalf("Localize = %q, want %q", got, want)
	}
	if got := err.Error(); got != i18n.T(i18n.SourceLocale, "auth.errors.internal")+": file does not exist" {
		t.Fatalf("Error() renders in the source locale, got %q", got)
	}
}

func TestWriteJSON(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "es")
	w := httptest.NewRecorder()
	apperr.WriteJSON(w, r, 0, errNotFound)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
	var b apperr.Body
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Code != "task.errors.not_found" || b.Error != i18n.T("es", "task.errors.not_found") {
		t.Fatalf("body %+v", b)
	}
}

func TestCausePlacement(t *testing.T) {
	c, err := i18n.Load(fstest.MapFS{
		"locales/en/t.json": {Data: []byte(`{"wrap": "{cause} (while {action})"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(i18n.ReplaceForTest(c))
	err = apperr.Wrap(fs.ErrNotExist, "t.wrap", "action", "x")
	if got := err.Error(); got != "file does not exist (while x)" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestErrorArgsRenderInTheMessageLocale(t *testing.T) {
	inner := apperr.NotFound("task.errors.not_found")
	err := apperr.New("updater.errors.helm_rollback_failed", "version", "1.2.3", "cause", "x", "rollback_err", inner)
	want := i18n.T("es", "updater.errors.helm_rollback_failed", "version", "1.2.3", "cause", "x",
		"rollback_err", i18n.T("es", "task.errors.not_found"))
	if got := apperr.Localize(err, "es"); got != want {
		t.Fatalf("Localize(es) = %q, want %q", got, want)
	}
}

func TestListRendersEachErrorInTheMessageLocale(t *testing.T) {
	list := apperr.List{Errs: []error{apperr.NotFound("task.errors.not_found"), nil, fs.ErrNotExist}, Sep: "; "}
	if got, want := list.Error(), i18n.T(i18n.SourceLocale, "task.errors.not_found")+"; file does not exist"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	err := apperr.Invalid("release.errors.manifest_invalid", "version", "1.2.3", "problems", list)
	want := i18n.T("es", "release.errors.manifest_invalid", "version", "1.2.3",
		"problems", i18n.T("es", "task.errors.not_found")+"; file does not exist")
	if got := apperr.Localize(err, "es"); got != want {
		t.Fatalf("Localize(es) = %q, want %q", got, want)
	}
}
