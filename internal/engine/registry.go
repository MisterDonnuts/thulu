package engine

import (
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const DefaultName = "claude-code"

var (
	regMu sync.RWMutex
	reg   = map[string]Engine{}
)

func Register(e Engine) {
	if e == nil {
		panic(i18n.Log("engine.errors.register_nil"))
	}
	name := e.Name()
	if name == "" {
		panic(i18n.Log("engine.errors.register_empty_name"))
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := reg[name]; dup {
		panic(i18n.Log("engine.errors.registered_twice", "name", name))
	}
	reg[name] = e
}

func Replace(e Engine) {
	if e == nil {
		panic(i18n.Log("engine.errors.replace_nil"))
	}
	name := e.Name()
	if name == "" {
		panic(i18n.Log("engine.errors.replace_empty_name"))
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg[name] = e
}

func Get(name string) (Engine, error) {
	if name == "" {
		name = DefaultName
	}
	regMu.RLock()
	defer regMu.RUnlock()
	e, ok := reg[name]
	if !ok {
		return nil, apperr.NotFound("engine.errors.not_registered", "name", name, "known", knownLocked())
	}
	return e, nil
}

func MustGet(name string) Engine {
	e, err := Get(name)
	if err != nil {
		panic(err)
	}
	return e
}

func Known() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return knownLocked()
}

func knownLocked() []string {
	out := make([]string, 0, len(reg))
	for k := range reg {
		out = append(out, k)
	}
	return out
}
