package credentials

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Sweeps are nearly free; re-introspection costs a provider call per credential. The dispatch gate
// still catches keys expiring between sweeps.
const (
	DefaultSweepInterval = 15 * time.Minute
	DefaultStaleAfter    = 6 * time.Hour
	// Bounds re-introspections per sweep so first boot on a large workspace does not fire every
	// provider call at once.
	DefaultSweepBatch = 25
)

type Change struct {
	Credential *Credential
	From       State
	To         State
	First      bool
}

// Not safe for concurrent Sweep calls; the daemon runs exactly one.
type Monitor struct {
	store    *Store
	registry *Registry
	perms    []string

	interval time.Duration
	stale    time.Duration
	batch    int
	outbound bool

	// No injectable clock on purpose: the store derives State from the system clock, so a monitor
	// clock would disagree with the rows. Tests move stored timestamps.

	seen map[string]State

	onChange func(context.Context, Change)
}

// perms must be the same catalog set the interactive validate uses, so both discover the same
// capabilities.
func NewMonitor(store *Store, registry *Registry, perms []string) *Monitor {
	outbound := monitorOutbound(os.Getenv)
	if !outbound {
		log.Printf("[credentials] %s", i18n.Log("credentials.log.monitor_off"))
	}
	return &Monitor{
		store:    store,
		registry: registry,
		perms:    perms,
		interval: DefaultSweepInterval,
		stale:    DefaultStaleAfter,
		batch:    DefaultSweepBatch,
		outbound: outbound,
	}
}

func monitorOutbound(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("OCTOPUS_CREDENTIAL_MONITOR"))) {
	case "off", "0", "false":
		return false
	default:
		return true
	}
}

func (m *Monitor) OnChange(fn func(context.Context, Change)) *Monitor {
	m.onChange = fn
	return m
}

func (m *Monitor) WithTimings(interval, stale time.Duration, batch int) *Monitor {
	if interval > 0 {
		m.interval = interval
	}
	if stale > 0 {
		m.stale = stale
	}
	if batch > 0 {
		m.batch = batch
	}
	return m
}

func (m *Monitor) Run(ctx context.Context) {
	m.Sweep(ctx)
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Sweep(ctx)
		}
	}
}

func (m *Monitor) Sweep(ctx context.Context) []Change {
	all, err := m.store.AllCredentials(ctx)
	if err != nil {
		log.Printf("[credentials] %s", i18n.Log("credentials.log.monitor_list_failed", "err", err))
		return nil
	}
	now := time.Now().UTC()
	firstSweep := m.seen == nil
	next := make(map[string]State, len(all))
	budget := m.batch
	var changes []Change

	for _, c := range all {
		if budget > 0 && m.due(c, now) {
			budget--
			if refreshed := m.revalidate(ctx, c); refreshed != nil {
				c = refreshed
			}
		}
		next[c.ID] = c.State

		prev, known := m.seen[c.ID]
		switch {
		case !known:
			if actionable(c.State) {
				changes = append(changes, Change{Credential: c, To: c.State, First: true})
			}
		case prev != c.State:
			changes = append(changes, Change{Credential: c, From: prev, To: c.State})
		}
	}
	m.seen = next

	if firstSweep {
		log.Printf("[credentials] %s", i18n.Log("credentials.log.monitor_first_sweep",
			"count", len(all), "attention", len(changes)))
	}
	for _, ch := range changes {
		if m.onChange != nil {
			m.onChange(ctx, ch)
		}
	}
	return changes
}

func (m *Monitor) due(c *Credential, now time.Time) bool {
	if c.Disabled || !m.outbound {
		return false
	}
	if c.ValidatedAt.IsZero() {
		return true
	}
	return now.Sub(c.ValidatedAt) >= m.stale
}

func (m *Monitor) revalidate(ctx context.Context, c *Credential) *Credential {
	payload, err := m.store.Payload(ctx, c.WorkspaceID, c.ID)
	if err != nil {
		log.Printf("[credentials] %s", i18n.Log("credentials.log.monitor_payload_failed", "id", c.ID, "err", err))
		return nil
	}
	res, err := m.registry.Introspect(ctx, c.Provider, c.Kind, payload, m.perms)
	if err != nil {
		// Unreachable is not a verdict: never overwrite state or capabilities, since empty
		// capabilities read as unrestricted (HasCapability).
		logx.Warn(err, "credentials.log.monitor_unreachable", "provider", c.Provider, "id", c.ID)
		if setErr := m.store.SetIntrospectionError(ctx, c.WorkspaceID, c.ID, IntrospectUnreachable); setErr != nil {
			log.Printf("[credentials] %s", i18n.Log("credentials.log.monitor_record_failure_failed", "id", c.ID, "err", setErr))
		}
		return nil
	}
	if storeErr := m.store.SetIntrospection(ctx, c.WorkspaceID, c.ID, res); storeErr != nil {
		log.Printf("[credentials] %s", i18n.Log("credentials.log.monitor_store_failed", "id", c.ID, "err", storeErr))
		return nil
	}
	fresh, readErr := m.store.Get(ctx, c.WorkspaceID, c.ID)
	if readErr != nil {
		log.Printf("[credentials] %s", i18n.Log("credentials.log.monitor_reload_failed", "id", c.ID, "err", readErr))
		return nil
	}
	return fresh
}

func actionable(s State) bool {
	switch s {
	case StateExpired, StateExpiring, StateInvalid:
		return true
	default:
		return false
	}
}

func (c Change) Summary() string {
	if c.First {
		return i18n.Text("credentials.audit.state_observed", "name", c.Credential.Name, "state", string(c.To))
	}
	return i18n.Text("credentials.audit.state_changed",
		"name", c.Credential.Name, "from", string(c.From), "to", string(c.To))
}

func (c Change) Degraded() bool { return actionable(c.To) }
