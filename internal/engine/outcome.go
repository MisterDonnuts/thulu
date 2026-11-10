package engine

import (
	"bytes"
	"sort"
	"strings"
)

const (
	OutcomeOK       = "ok"
	OutcomeMaxTurns = "max_turns"
	OutcomeError    = "error"
)

// Printed by the adapters' engine_run (images/snippets/engines/<name>.sh).
const (
	SessionNative     = "native"
	SessionRehydrated = "rehydrated"
	SessionFresh      = "fresh"
)

const SessionLineType = "octopus.session"

// preferred can be wrong (a profile may route to another engine); parsers only recognize their own
// wire format,
// so the first that finds a result is right.
func ResultOutcome(stream []byte, preferred string) string {
	ev, ok := LastResult(stream, preferred)
	if !ok {
		return ""
	}
	return ev.Outcome
}

func LastResult(stream []byte, preferred string) (Event, bool) {
	if preferred == "" {
		preferred = DefaultName
	}
	names := Known()
	sort.Strings(names)
	order := append([]string{preferred}, names...)
	for i, name := range order {
		if i > 0 && name == preferred {
			continue
		}
		eng, err := Get(name)
		if err != nil {
			continue
		}
		var last Event
		for ev := range eng.ParseStream(bytes.NewReader(stream)) {
			if ev.Kind == EventResult {
				last = ev
			}
		}
		if last.Outcome != "" {
			return last, true
		}
	}
	return Event{}, false
}

func PriceFor(model string) (ModelPrice, bool) {
	if model == "" {
		return ModelPrice{}, false
	}
	prices := Prices()
	for _, p := range prices {
		if p.Model == model {
			return p, true
		}
	}
	lower := strings.ToLower(model)
	for _, p := range prices {
		if p.Family && strings.Contains(lower, strings.ToLower(p.Model)) {
			return p, true
		}
	}
	return ModelPrice{}, false
}

func Prices() []ModelPrice {
	names := Known()
	sort.Strings(names)
	seen := map[string]bool{}
	var out []ModelPrice
	for _, name := range names {
		eng, err := Get(name)
		if err != nil {
			continue
		}
		for _, p := range eng.Capabilities().Pricing {
			if p.Model == "" || seen[p.Model] {
				continue
			}
			seen[p.Model] = true
			out = append(out, p)
		}
	}
	return out
}
