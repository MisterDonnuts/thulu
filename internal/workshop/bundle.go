package workshop

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Bump FormatVersion only when an older reader would misread the new shape; Parse refuses any other
// version.
const (
	Format        = "octopus-workshop"
	FormatVersion = 1
)

// The largest real prompt is tens of kilobytes; these bound mistakes and abuse, not real catalogs.
const (
	MaxAgents = 50
	MaxSkills = 200
	MaxFiles  = 2000
	MaxBytes  = 3 << 20
)

const encodingBase64 = "base64"

type File struct {
	Path     string `json:"path"`
	Body     string `json:"body"`
	Encoding string `json:"encoding,omitempty"`
}

type Agent struct {
	Revision   int             `json:"revision,omitempty"`
	Tags       []string        `json:"tags,omitempty"`
	Definition types.AgentType `json:"definition"`
	Files      []File          `json:"files,omitempty"`
}

// dir is a path on the exporting install's disk and must not leave in a bundle.
func (a Agent) MarshalJSON() ([]byte, error) {
	def, err := json.Marshal(a.Definition)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(def, &m); err != nil {
		return nil, err
	}
	delete(m, "dir")
	type wire struct {
		Revision   int            `json:"revision,omitempty"`
		Tags       []string       `json:"tags,omitempty"`
		Definition map[string]any `json:"definition"`
		Files      []File         `json:"files,omitempty"`
	}
	return json.Marshal(wire{Revision: a.Revision, Tags: a.Tags, Definition: m, Files: a.Files})
}

type Skill struct {
	Name     string `json:"name"`
	Revision int    `json:"revision,omitempty"`
	Files    []File `json:"files"`
}

type Bundle struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	Agents     []Agent   `json:"agents"`
	Skills     []Skill   `json:"skills"`
}

type SkillMeta struct {
	Name         string            `yaml:"name" json:"name"`
	Description  string            `yaml:"description" json:"description,omitempty"`
	Descriptions map[string]string `yaml:"descriptions" json:"-"`
	Requires     []string          `yaml:"requires" json:"requires,omitempty"`
	Tags         []string          `yaml:"tags" json:"tags,omitempty"`
	Version      int               `yaml:"version" json:"-"`
	MCP          string            `yaml:"mcp" json:"mcp,omitempty"`
}

type AgentItem struct {
	Def          types.AgentType
	Revision     int
	Tags         []string
	Files        map[string][]byte
	Descriptions map[string]string
}

type SkillItem struct {
	Name     string
	Revision int
	Meta     SkillMeta
	Files    map[string][]byte
}

type Set struct {
	Agents       []AgentItem
	Skills       []SkillItem
	Rejected     []Rejected
	SkippedTools int
}

type Rejected struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Path string `json:"path"`
}

func kindKey(kind, agentKey, skillKey string) string {
	if kind == KindAgent {
		return agentKey
	}
	return skillKey
}

func (s *Set) Skill(name string) (SkillItem, bool) {
	for i := range s.Skills {
		sk := &s.Skills[i]
		if sk.Name == name {
			return *sk, true
		}
	}
	return SkillItem{}, false
}

func (s *Set) Agent(name string) (AgentItem, bool) {
	for i := range s.Agents {
		a := &s.Agents[i]
		if a.Def.Name == name {
			return *a, true
		}
	}
	return AgentItem{}, false
}

func Parse(raw []byte) (*Set, error) {
	if len(raw) > MaxBytes*2 {
		return nil, apperr.TooLarge("workshop.errors.bundle_too_large")
	}
	raw = agentstore.RenameLegacy(raw) // definitions and plain-text files; base64 bodies in decode
	var probe struct {
		Format  string          `json:"format"`
		Version int             `json:"version"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, apperr.Invalid("workshop.errors.not_json")
	}
	switch {
	case probe.Format == Format:
		if probe.Version != FormatVersion {
			return nil, apperr.Invalid("workshop.errors.version_unsupported", "version", probe.Version, "want", FormatVersion)
		}
		var b Bundle
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, apperr.Invalid("workshop.errors.not_json")
		}
		return Decode(b)
	case probe.Format == "" && probe.Content != nil:
		var p agentstore.Pack
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, apperr.Invalid("workshop.errors.not_json")
		}
		return fromPack(p)
	}
	return nil, apperr.Invalid("workshop.errors.not_a_bundle")
}

func Decode(b Bundle) (*Set, error) {
	if len(b.Agents) > MaxAgents || len(b.Skills) > MaxSkills {
		return nil, apperr.TooLarge("workshop.errors.too_many_items", "agents", MaxAgents, "skills", MaxSkills)
	}
	set := &Set{}
	var budget budget
	for i := range b.Agents {
		a := &b.Agents[i]
		files, secret, err := budget.decode(KindAgent, a.Definition.Name, a.Files)
		if err != nil {
			return nil, err
		}
		if secret != "" {
			set.Rejected = append(set.Rejected, Rejected{Kind: KindAgent, Name: a.Definition.Name, Path: secret})
			continue
		}
		item, err := newAgentItem(a.Definition, a.Revision, a.Tags, files)
		if err != nil {
			return nil, err
		}
		set.Agents = append(set.Agents, item)
	}
	for i := range b.Skills {
		sk := &b.Skills[i]
		files, secret, err := budget.decode(KindSkill, sk.Name, sk.Files)
		if err != nil {
			return nil, err
		}
		if secret != "" {
			set.Rejected = append(set.Rejected, Rejected{Kind: KindSkill, Name: sk.Name, Path: secret})
			continue
		}
		item, err := NewSkillItem(sk.Name, sk.Revision, files)
		if err != nil {
			return nil, err
		}
		set.Skills = append(set.Skills, item)
	}
	if err := set.checkUnique(); err != nil {
		return nil, err
	}
	return set, nil
}

type budget struct {
	files int
	bytes int
}

func (bg *budget) decode(kind, owner string, in []File) (files map[string][]byte, secret string, err error) {
	badFile := kindKey(kind, "workshop.errors.agent_bad_file", "workshop.errors.skill_bad_file")
	out := make(map[string][]byte, len(in))
	for _, f := range in {
		var body []byte
		switch f.Encoding {
		case "":
			body = []byte(f.Body)
		case encodingBase64:
			b, err := base64.StdEncoding.DecodeString(f.Body)
			if err != nil {
				return nil, "", apperr.Invalid(badFile, "name", owner, "path", f.Path)
			}
			body = agentstore.RenameLegacy(b)
		default:
			return nil, "", apperr.Invalid(badFile, "name", owner, "path", f.Path)
		}
		bg.files++
		bg.bytes += len(body)
		if bg.files > MaxFiles || bg.bytes > MaxBytes {
			return nil, "", apperr.TooLarge("workshop.errors.bundle_too_large")
		}
		if _, dup := out[f.Path]; dup {
			return nil, "", apperr.Invalid(
				kindKey(kind, "workshop.errors.agent_duplicate_file", "workshop.errors.skill_duplicate_file"),
				"name", owner, "path", f.Path)
		}
		if err := agentstore.ValidatePath(f.Path); err != nil {
			return nil, "", apperr.Invalid(badFile, "name", owner, "path", f.Path)
		}
		if secret == "" && agentstore.ScanForSecrets(body) != nil {
			secret = f.Path
		}
		out[f.Path] = body
	}
	if secret != "" {
		return nil, secret, nil
	}
	return out, "", nil
}

func newAgentItem(def types.AgentType, rev int, tags []string, files map[string][]byte) (AgentItem, error) {
	def.Dir = ""
	def.SystemSkills = nil
	if err := agentstore.ValidateAgentName(def.Name); err != nil {
		return AgentItem{}, apperr.Invalid("workshop.errors.bad_agent_name", "name", def.Name)
	}
	if def.Type == "" {
		def.Type = def.Name
	}
	// Old-shape policies (a level without a worker) load as off, the way the store loads them.
	def.TokenOptimizer = def.TokenOptimizer.OrOff()
	if err := def.TokenOptimizer.Validate(engineRegistered); err != nil {
		return AgentItem{}, apperr.Wrap(err, "workshop.errors.bad_agent_token_optimizer", "name", def.Name)
	}
	return AgentItem{Def: def, Revision: rev, Tags: tags, Files: files}, nil
}

func engineRegistered(name string) bool {
	_, err := engine.Get(name)
	return err == nil
}

func NewSkillItem(name string, rev int, files map[string][]byte) (SkillItem, error) {
	if err := agentstore.ValidateName(name); err != nil {
		return SkillItem{}, apperr.Invalid("workshop.errors.bad_skill_name", "name", name)
	}
	metaBody, okMeta := files[agentstore.SkillMetaFile]
	_, okPrompt := files[agentstore.SkillPromptFile]
	if !okMeta || !okPrompt {
		return SkillItem{}, apperr.Invalid("workshop.errors.skill_incomplete", "name", name)
	}
	var meta SkillMeta
	if err := yaml.Unmarshal(metaBody, &meta); err != nil {
		return SkillItem{}, apperr.Invalid("workshop.errors.bad_skill_meta", "name", name)
	}
	if meta.Name != "" && meta.Name != name {
		return SkillItem{}, apperr.Invalid("workshop.errors.skill_name_mismatch", "name", name, "meta", meta.Name)
	}
	meta.Name = name
	if rev == 0 {
		rev = meta.Version
	}
	return SkillItem{Name: name, Revision: rev, Meta: meta, Files: files}, nil
}

func (s *Set) checkUnique() error {
	seen := map[string]bool{}
	for i := range s.Agents {
		a := &s.Agents[i]
		if seen["a/"+a.Def.Name] {
			return apperr.Invalid("workshop.errors.agent_duplicate_item", "name", a.Def.Name)
		}
		seen["a/"+a.Def.Name] = true
	}
	for i := range s.Skills {
		sk := &s.Skills[i]
		if seen["s/"+sk.Name] {
			return apperr.Invalid("workshop.errors.skill_duplicate_item", "name", sk.Name)
		}
		seen["s/"+sk.Name] = true
	}
	for _, r := range s.Rejected {
		key := "a/" + r.Name
		if r.Kind == KindSkill {
			key = "s/" + r.Name
		}
		if seen[key] {
			return apperr.Invalid(kindKey(r.Kind, "workshop.errors.agent_duplicate_item", "workshop.errors.skill_duplicate_item"), "name", r.Name)
		}
		seen[key] = true
	}
	return nil
}

// MCP manuals are install-wide and shared by every workspace; a workspace import must never write
// them.
func fromPack(p agentstore.Pack) (*Set, error) {
	if p.Version != agentstore.PackVersion {
		return nil, apperr.Invalid("workshop.errors.version_unsupported", "version", p.Version, "want", agentstore.PackVersion)
	}
	b := Bundle{Format: Format, Version: FormatVersion}
	agentFiles := map[string][]File{}
	skillFiles := map[string][]File{}
	var skillOrder []string
	tools := map[string]bool{}
	for _, f := range p.Content {
		wire := File{Path: f.Path, Body: f.Body, Encoding: encodingBase64}
		switch f.Kind {
		case agentstore.KindAgent:
			agentFiles[f.Owner] = append(agentFiles[f.Owner], wire)
		case agentstore.KindSkill:
			if _, ok := skillFiles[f.Owner]; !ok {
				skillOrder = append(skillOrder, f.Owner)
			}
			skillFiles[f.Owner] = append(skillFiles[f.Owner], wire)
		case agentstore.KindTool:
			tools[f.Owner] = true
		}
	}
	for i := range p.Agents {
		def := p.Agents[i].AgentType
		b.Agents = append(b.Agents, Agent{Definition: def, Files: agentFiles[def.Name]})
	}
	for _, name := range skillOrder {
		b.Skills = append(b.Skills, Skill{Name: name, Files: skillFiles[name]})
	}
	set, err := Decode(b)
	if err != nil {
		return nil, err
	}
	set.SkippedTools = len(tools)
	return set, nil
}

func Encode(s *Set, at time.Time) Bundle {
	b := Bundle{Format: Format, Version: FormatVersion, ExportedAt: at.UTC(), Agents: []Agent{}, Skills: []Skill{}}
	for i := range s.Agents {
		a := &s.Agents[i]
		b.Agents = append(b.Agents, Agent{Revision: a.Revision, Tags: a.Tags, Definition: a.Def, Files: encodeFiles(a.Files)})
	}
	for i := range s.Skills {
		sk := &s.Skills[i]
		b.Skills = append(b.Skills, Skill{Name: sk.Name, Revision: sk.Revision, Files: encodeFiles(sk.Files)})
	}
	sort.Slice(b.Agents, func(i, j int) bool { return b.Agents[i].Definition.Name < b.Agents[j].Definition.Name })
	sort.Slice(b.Skills, func(i, j int) bool { return b.Skills[i].Name < b.Skills[j].Name })
	return b
}

func encodeFiles(files map[string][]byte) []File {
	out := make([]File, 0, len(files))
	for _, p := range sortedPaths(files) {
		body := files[p]
		if utf8.Valid(body) {
			out = append(out, File{Path: p, Body: string(body)})
		} else {
			out = append(out, File{Path: p, Body: base64.StdEncoding.EncodeToString(body), Encoding: encodingBase64})
		}
	}
	return out
}

func sortedPaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func filesHash(files map[string][]byte) string {
	h := sha256.New()
	for _, p := range sortedPaths(files) {
		sum := sha256.Sum256(files[p])
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The store returns empty maps and lists where a bundle has none; drop empties before hashing or an
// untouched install reads as edited.
func defHash(def types.AgentType) string {
	def.Dir = ""
	def.SystemSkills = nil
	// The store writes the default skills mode out; "" must hash the same.
	if def.SkillsMode == types.SkillsModeAuto {
		def.SkillsMode = ""
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	canon, err := json.Marshal(dropEmpty(v))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

func dropEmpty(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range t {
			if c := dropEmpty(val); c != nil {
				out[k] = c
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(t) == 0 {
			return nil
		}
		out := make([]any, 0, len(t))
		for _, val := range t {
			out = append(out, dropEmpty(val))
		}
		return out
	case string:
		if t == "" {
			return nil
		}
	case bool:
		if !t {
			return nil
		}
	case float64:
		if t == 0 {
			return nil
		}
	}
	return v
}

func agentHash(def types.AgentType, files map[string][]byte) string {
	return defHash(def) + filesHash(files)
}

func SkillRefs(def types.AgentType) []string {
	seen := map[string]bool{}
	var out []string
	add := func(byStage map[string][]string) {
		for _, stage := range sortedKeys(byStage) {
			for _, n := range byStage[stage] {
				n = strings.TrimSpace(n)
				if n != "" && !seen[n] {
					seen[n] = true
					out = append(out, n)
				}
			}
		}
	}
	add(def.SkillsByStage)
	if def.Auditor != nil {
		add(def.Auditor.SkillsByStage)
	}
	return out
}

func MCPRefs(def types.AgentType) []string {
	seen := map[string]bool{}
	var out []string
	for _, stage := range sortedKeys(def.MCPByStage) {
		for _, e := range def.MCPByStage[stage] {
			n := types.MCPServerName(e)
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

func StageRefs(def types.AgentType) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, st := range def.ValidStages {
		add(string(st))
	}
	for _, k := range sortedKeys(def.SkillsByStage) {
		add(k)
	}
	for _, k := range sortedKeys(def.MCPByStage) {
		add(k)
	}
	for k := range def.TurnsByStage {
		add(k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Edit the YAML node tree, not a struct, to keep the author's comments and key order.
func renameSkillMeta(body []byte, name string, renames map[string]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, apperr.Invalid("workshop.errors.bad_skill_meta", "name", name)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, apperr.Invalid("workshop.errors.bad_skill_meta", "name", name)
	}
	m := doc.Content[0]
	hasName := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, val := m.Content[i], m.Content[i+1]
		switch key.Value {
		case "name":
			val.Value = name
			hasName = true
		case "requires":
			for _, item := range val.Content {
				if to, ok := renames[item.Value]; ok {
					item.Value = to
				}
			}
		}
	}
	if !hasName {
		m.Content = append([]*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "name"},
			{Kind: yaml.ScalarNode, Value: name},
		}, m.Content...)
	}
	return encodeSkillMeta(&doc, name)
}

func encodeSkillMeta(doc *yaml.Node, name string) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, apperr.Invalid("workshop.errors.bad_skill_meta", "name", name)
	}
	if err := enc.Close(); err != nil {
		return nil, apperr.Invalid("workshop.errors.bad_skill_meta", "name", name)
	}
	return buf.Bytes(), nil
}

// With nothing to localize, return body byte for byte: install hashes depend on it.
func localizeSkillMeta(body []byte, name, locale string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, apperr.Invalid("workshop.errors.bad_skill_meta", "name", name)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, apperr.Invalid("workshop.errors.bad_skill_meta", "name", name)
	}
	m := doc.Content[0]
	desc, want := skillMetaDescription(m, locale)
	if want == "" || (desc != nil && desc.Value == want) {
		return body, nil
	}
	if desc == nil {
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "description"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: want})
	} else {
		desc.Value = want
	}
	return encodeSkillMeta(&doc, name)
}

func skillMetaDescription(m *yaml.Node, locale string) (desc *yaml.Node, want string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, val := m.Content[i], m.Content[i+1]
		switch key.Value {
		case "description":
			desc = val
		case "descriptions":
			if val.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(val.Content); j += 2 {
				if val.Content[j].Value == locale {
					want = val.Content[j+1].Value
				}
			}
		}
	}
	return desc, want
}

func skillFilesIn(files map[string][]byte, name, locale string) (map[string][]byte, error) {
	body, ok := files[agentstore.SkillMetaFile]
	if !ok {
		return files, nil
	}
	meta, err := localizeSkillMeta(body, name, locale)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(meta, body) {
		return files, nil
	}
	return withFile(files, agentstore.SkillMetaFile, meta), nil
}

func sameSkill(have, files map[string][]byte, name string, locales map[string]string) (bool, error) {
	cur := filesHash(have)
	if cur == filesHash(files) {
		return true, nil
	}
	for _, locale := range sortedKeys(locales) {
		in, err := skillFilesIn(files, name, locale)
		if err != nil {
			return false, err
		}
		if filesHash(in) == cur {
			return true, nil
		}
	}
	return false, nil
}
