package task

import (
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// The optional verdict.md fields. Only verdict is mandatory and format never blocks a task: each
// field takes what it can read and drops the rest, so a list where a string goes, or the other way
// round, never fails the whole front matter.
type verdictFacts struct {
	Headline    looseText       `yaml:"headline"`
	NeedsPerson needsPersonList `yaml:"needs_person"`
	Delta       looseList       `yaml:"delta"`
	Next        looseText       `yaml:"next"`
	// With verdict block: what the agent needs from a person, in one or two sentences. It opens
	// the stop thread in Comments; the report stays in Reports.
	Ask looseText `yaml:"ask"`
}

// What a stage verdict asks of a person: a case or an item (ref), what to do (ask) and why an agent
// could not (why, one of NeedsPersonWhys or "other").
type NeedsPerson struct {
	Ref string `json:"ref"`
	Ask string `json:"ask"`
	Why string `json:"why"`
}

const NeedsPersonOther = "other"

// Why an agent cannot do it itself; anything else reads as "other".
var NeedsPersonWhys = map[string]bool{
	"real_account": true, "data_write": true, "device": true, "decision": true, "credential": true,
}

func needsPersonWhy(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if NeedsPersonWhys[s] {
		return s
	}
	return NeedsPersonOther
}

// A person reads the headline on a card: one line, at most this many characters.
const maxVerdictHeadline = 90

// VerdictMeta is a stage_verdict report's front matter as the API lists it with the report.
type VerdictMeta struct {
	Verdict     string        `json:"verdict"`
	Reason      string        `json:"reason,omitempty"`
	KickBackTo  string        `json:"kick_back_to,omitempty"`
	Headline    string        `json:"headline,omitempty"`
	NeedsPerson []NeedsPerson `json:"needs_person,omitempty"`
	// A string or a list, as the agent wrote it.
	Delta any    `json:"delta,omitempty"`
	Next  string `json:"next,omitempty"`
	Ask   string `json:"ask,omitempty"`
}

// VerdictMetaOf reads a verdict.md body; nil when it has no verdict.
func VerdictMetaOf(body string) *VerdictMeta {
	v := parseVerdict(body)
	if v.Verdict == "" {
		return nil
	}
	return v.meta()
}

func (v stageVerdict) meta() *VerdictMeta {
	m := &VerdictMeta{
		Verdict: v.Verdict, KickBackTo: strings.TrimSpace(v.KickBackTo),
		Headline: v.headline(), NeedsPerson: v.NeedsPerson, Next: string(v.Next),
		Ask: strings.TrimSpace(string(v.Ask)),
	}
	if strings.TrimSpace(v.Reason) != "" {
		m.Reason = v.reasonCode()
	}
	switch {
	case v.Delta.scalar && len(v.Delta.items) == 1:
		m.Delta = v.Delta.items[0]
	case len(v.Delta.items) > 0:
		m.Delta = v.Delta.items
	}
	return m
}

// The agent's headline, else the first line of the body; one line, cut to maxVerdictHeadline.
func (v stageVerdict) headline() string {
	h := strings.TrimSpace(string(v.Headline))
	if h == "" {
		h = firstBodyLine(v.Body)
	}
	return cutRunes(h, maxVerdictHeadline)
}

func firstBodyLine(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#>*- "))
		if line != "" {
			return line
		}
	}
	return ""
}

func cutRunes(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:limit-1])) + "…"
}

// A scalar, or the scalars of a list joined by "; ".
type looseText string

func (t *looseText) UnmarshalYAML(n *yaml.Node) error {
	*t = looseText(strings.Join(scalars(n), "; "))
	return nil
}

// A string or a list of strings; scalar remembers which one the agent wrote.
type looseList struct {
	items  []string
	scalar bool
}

func (l *looseList) UnmarshalYAML(n *yaml.Node) error {
	l.items, l.scalar = scalars(n), n.Kind == yaml.ScalarNode
	return nil
}

// scalars reads the text of a node: a scalar, the scalars of a list, or "key: value" for the
// entries of a mapping inside a list. Anything deeper is dropped.
func scalars(n *yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		if s := strings.TrimSpace(n.Value); s != "" {
			return []string{s}
		}
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			switch c.Kind {
			case yaml.ScalarNode:
				out = append(out, scalars(c)...)
			case yaml.MappingNode:
				if s := mappingText(c); s != "" {
					out = append(out, s)
				}
			case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode:
				// A list inside a list, or an alias: deeper than a verdict needs.
			}
		}
		return out
	case yaml.MappingNode:
		if s := mappingText(n); s != "" {
			return []string{s}
		}
	case yaml.DocumentNode, yaml.AliasNode:
	}
	return nil
}

func mappingText(n *yaml.Node) string {
	var parts []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		if v := strings.Join(scalars(n.Content[i+1]), ", "); v != "" {
			parts = append(parts, n.Content[i].Value+": "+v)
		}
	}
	return strings.Join(parts, "; ")
}

// needs_person: a list of {ref, ask, why}. A plain string item is an ask with no ref; an item with
// neither ref nor ask is dropped.
type needsPersonList []NeedsPerson

func (l *needsPersonList) UnmarshalYAML(n *yaml.Node) error {
	items := n.Content
	if n.Kind != yaml.SequenceNode {
		items = []*yaml.Node{n}
	}
	var out []NeedsPerson
	for _, c := range items {
		var p NeedsPerson
		switch c.Kind {
		case yaml.ScalarNode:
			p.Ask = strings.TrimSpace(c.Value)
		case yaml.MappingNode:
			for i := 0; i+1 < len(c.Content); i += 2 {
				val := strings.Join(scalars(c.Content[i+1]), "; ")
				switch strings.ToLower(c.Content[i].Value) {
				case "ref", "id", "case":
					p.Ref = val
				case "ask", "what":
					p.Ask = val
				case "why", "reason":
					p.Why = val
				}
			}
		default:
			continue
		}
		if p.Ref == "" && p.Ask == "" {
			continue
		}
		p.Why = needsPersonWhy(p.Why)
		out = append(out, p)
	}
	*l = out
	return nil
}
