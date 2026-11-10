package engine_test

import (
	"context"
	"io"
	"iter"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	// blank import: registers "claude-code" so the default lookup works.
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
)

func TestDefault_ResolvesClaudeCode(t *testing.T) {
	e, err := engine.Get("")
	if err != nil {
		t.Fatalf("Get(\"\") returned error: %v", err)
	}
	if e.Name() != engine.DefaultName {
		t.Errorf("default name = %q, want %q", e.Name(), engine.DefaultName)
	}
}

func TestGet_Unknown_ReturnsError(t *testing.T) {
	_, err := engine.Get("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown engine, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error message %q should name the engine", err.Error())
	}
}

func TestKnown_ContainsClaudeCode(t *testing.T) {
	names := engine.Known()
	if len(names) == 0 {
		t.Fatal("Known() returned empty")
	}
	var found bool
	for _, n := range names {
		if n == "claude-code" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Known() = %v, missing claude-code", names)
	}
}

type fakeEngine struct{ name string }

func (f *fakeEngine) Name() string { return f.name }
func (f *fakeEngine) ParseStream(io.Reader) iter.Seq[engine.Event] {
	return func(func(engine.Event) bool) {}
}
func (f *fakeEngine) AuthEnv(context.Context) []string  { return nil }
func (f *fakeEngine) Capabilities() engine.Capabilities { return engine.Capabilities{} }

func TestRegister_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register(nil) should panic")
		}
	}()
	engine.Register(nil)
}

func TestRegister_EmptyNamePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register with empty name should panic")
		}
	}()
	engine.Register(&fakeEngine{name: ""})
}

func TestRegister_DuplicatePanics(t *testing.T) {
	engine.Register(&fakeEngine{name: "test-dup-once"})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Re-registering a name should panic")
		}
	}()
	engine.Register(&fakeEngine{name: "test-dup-once"})
}

func TestMustGet_Unknown_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustGet of unknown engine should panic")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("panic value = %T (%v), want error", r, r)
		}
		if !strings.Contains(err.Error(), "nope-not-registered") {
			t.Errorf("panic error %q should name the missing engine", err.Error())
		}
	}()
	engine.MustGet("nope-not-registered")
}
