package mcp

import (
	"context"
	"sync"
)

type Forwarder struct {
	mu   sync.RWMutex
	urls map[string]string
	// Keyed by server name, not env key.
	credential map[string]bool
	withheld   map[string]bool
	// Keyed by server name, not env key.
	transport map[string]string
	grants    GrantRecorder
}

type GrantRecorder interface {
	RecordRunGrant(ctx context.Context, ref, agent, stage string, g RunGrant) error
}

func NewForwarder() *Forwarder {
	return &Forwarder{
		urls:       make(map[string]string),
		credential: make(map[string]bool),
		withheld:   make(map[string]bool),
		transport:  make(map[string]string),
	}
}

func (f *Forwarder) SetGrantRecorder(r GrantRecorder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants = r
}

func (f *Forwarder) grantRecorder() GrantRecorder {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.grants
}

func (f *Forwarder) SetTransport(name, transport string) {
	if name == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if transport == "" || transport == TransportSSE {
		delete(f.transport, name)
		return
	}
	f.transport[name] = transport
}

type Upstream struct {
	Name               string
	URL                string
	Transport          string
	ForwardsCredential bool
}

func (f *Forwarder) Lookup(name string) (Upstream, bool) {
	key := NameToEnvKey(name)
	f.mu.RLock()
	defer f.mu.RUnlock()
	u, ok := f.urls[key]
	if !ok || f.withheld[key] {
		return Upstream{}, false
	}
	t := f.transport[name]
	if t == "" {
		t = TransportSSE
	}
	return Upstream{Name: name, URL: u, Transport: t, ForwardsCredential: f.credential[name]}, true
}

// Used on MCP_THULU_BROWSER_URL in per-run browser mode: a shared thulu-browser runs every caller's
// scripts in one process.
func (f *Forwarder) Withhold(key string) {
	if key == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withheld[key] = true
}

func (f *Forwarder) SetForwardsCredential(name string, on bool) {
	if name == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.credential[name] = true
		return
	}
	delete(f.credential, name)
}

func (f *Forwarder) Set(key, url string) {
	if key == "" || url == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls[key] = url
}

func (f *Forwarder) SetMany(urls map[string]string) {
	if len(urls) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range urls {
		if k != "" && v != "" {
			f.urls[k] = v
		}
	}
}

func (f *Forwarder) Delete(key string) {
	if key == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.urls, key)
}

func (f *Forwarder) Snapshot() map[string]string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]string, len(f.urls))
	for k, v := range f.urls {
		out[k] = v
	}
	return out
}
