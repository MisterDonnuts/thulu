package agentstore

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

var legacyNames = []struct {
	re *regexp.Regexp
	to []byte
}{
	{regexp.MustCompile(`OCTOPUS_MCP_ARGUS_`), []byte("OCTOPUS_MCP_")},
	{regexp.MustCompile(`octopus-mcp-argus-`), []byte("octopus-mcp-")},
	{regexp.MustCompile(`MCP_ARGUS_(GITHUB|BROWSER|CLOUD|FIGMA|SANDBOX)_`), []byte("MCP_THULU_${1}_")},
	{regexp.MustCompile(`argus-(github|browser|cloud|figma|sandbox)`), []byte("thulu-$1")},
	{regexp.MustCompile(`\bargus\.([a-z_])`), []byte("thulu.$1")},
	{regexp.MustCompile(`\.argus\b`), []byte(".thulu")},
}

// Names used before 1.0. A bundle a 0.x release exported (workshop export, octopus export, an
// agents pack) names the MCP servers argus-<x>, label keys argus.* and the project directory
// .argus. Imports pass what they read through here, which rewrites them as the data migration
// agentstore 18 (rename_migration.go) rewrote stored rows. Bytes that are not UTF-8 come back
// unchanged.
func RenameLegacy(b []byte) []byte {
	if !utf8.Valid(b) {
		return b
	}
	for _, r := range legacyNames {
		b = r.re.ReplaceAll(b, r.to)
	}
	return b
}

func renameLegacyPack(p *Pack) error {
	p.Content = append([]PackedFile(nil), p.Content...)
	for i := range p.Content {
		f := &p.Content[i]
		f.Owner = string(RenameLegacy([]byte(f.Owner)))
		body, err := base64.StdEncoding.DecodeString(f.Body)
		if err != nil {
			continue // ImportPack reports it
		}
		f.Body = base64.StdEncoding.EncodeToString(RenameLegacy(body))
	}
	raw, err := json.Marshal(p.Agents)
	if err != nil {
		return err
	}
	p.Agents = nil
	return json.Unmarshal(RenameLegacy(raw), &p.Agents)
}
