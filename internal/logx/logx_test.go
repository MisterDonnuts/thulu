package logx

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

func capture(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	out := log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	fn()
	return buf.String()
}

func TestWarnNilIsSilent(t *testing.T) {
	got := capture(t, func() { Warn(nil, "common.log.close_failed", "name", "x") })
	if got != "" {
		t.Fatalf("expected no output for nil error, got %q", got)
	}
}

func TestWarnLogsCatalogMessageAndError(t *testing.T) {
	got := capture(t, func() { Warn(errors.New("boom"), "common.log.close_failed", "name", "task-1") })
	if !strings.Contains(got, "warn: close task-1: boom") {
		t.Fatalf("unexpected log output: %q", got)
	}
}

func TestSafeStripsControlChars(t *testing.T) {
	cases := map[string]string{
		"plain/path":            "plain/path",
		"inject\nfake log line": "injectfake log line",
		"carriage\rreturn":      "carriagereturn",
		"tab\tseparated":        "tabseparated",
		"null\x00byte":          "nullbyte",
		"escape\x1b[31mred":     "escape[31mred",
		"unicode-ok-áéí":        "unicode-ok-áéí",
	}
	for in, want := range cases {
		if got := Safe(in); got != want {
			t.Errorf("Safe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeForgedLineIsNeutralized(t *testing.T) {
	forged := "/x\n[api] FAKE admin login from 1.2.3.4"
	if strings.Contains(Safe(forged), "\n") {
		t.Fatalf("Safe left a newline in %q", Safe(forged))
	}
}

type fakeCloser struct {
	called bool
	err    error
}

func (f *fakeCloser) Close() error { f.called = true; return f.err }

func TestCloseLogsError(t *testing.T) {
	fc := &fakeCloser{err: errors.New("boom")}
	got := capture(t, func() { Close(fc, "db handle") })
	if !fc.called {
		t.Fatal("expected Close to be invoked")
	}
	if !strings.Contains(got, "warn: close db handle: boom") {
		t.Fatalf("unexpected log output: %q", got)
	}
}

func TestCloseNilCloserIsSilent(t *testing.T) {
	got := capture(t, func() { Close(nil, "noop") })
	if got != "" {
		t.Fatalf("expected no output for nil closer, got %q", got)
	}
}

func TestCloseSuccessIsSilent(t *testing.T) {
	got := capture(t, func() { Close(&fakeCloser{}, "ok") })
	if got != "" {
		t.Fatalf("expected no output on success, got %q", got)
	}
}
