package api

import (
	"archive/tar"
	"bytes"
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestSkillMetaFlattensTheDescription(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, yaml, want string }{
		{"quoted", "description: \"Use when: notes\"\nenforcement: always\n",
			"enforcement=always\ndescription=Use when: notes\n"},
		{"folded", "description: >-\n  first line\n  second line\n",
			"enforcement=\ndescription=first line second line\n"},
		{"literal", "description: |\n  one\n  two\nenforcement: on_demand\n",
			"enforcement=on_demand\ndescription=one two\n"},
		{"absent", "name: notes\n", "enforcement=\ndescription=\n"},
	}
	for _, c := range cases {
		if got := string(skillMeta([]byte(c.yaml))); got != c.want {
			t.Errorf("%s: skillMeta = %q, want %q", c.name, got, c.want)
		}
	}
	if got := skillMeta([]byte("description: [unclosed\n")); got != nil {
		t.Errorf("broken YAML = %q, want nil so the script falls back", got)
	}
}

type fakeSkillSource struct{ files []contentFile }

func (f fakeSkillSource) Files(_ context.Context, kind, owner string) ([]contentFile, error) {
	if kind == agentstore.KindSkill && owner == "notes" {
		return f.files, nil
	}
	return nil, nil
}

func TestBundleCarriesTheSkillMeta(t *testing.T) {
	t.Parallel()
	src := fakeSkillSource{files: []contentFile{
		{Path: "prompt.md", Body: []byte("rules")},
		{Path: "skill.yaml", Body: []byte("description: \"a: b\"\n")},
	}}
	at := types.AgentType{Name: "developer", SkillsByStage: map[string][]string{"review": {"notes"}}}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := fillBundle(context.Background(), tw, src, at, "review"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	tr := tar.NewReader(&buf)
	found := ""
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Name == "skills/notes/"+skillMetaFile {
			var b bytes.Buffer
			_, _ = b.ReadFrom(tr)
			found = b.String()
		}
	}
	if found != "enforcement=\ndescription=a: b\n" {
		t.Fatalf("skills/notes/%s = %q", skillMetaFile, found)
	}
}
