package builtin

import (
	"bufio"
	"bytes"
	"io/fs"
	"regexp"
	"strconv"
	"testing"
	"testing/fstest"
)

// The system skills and the native tools' manuals and descriptors are what the model reads on
// every run, and tool.yaml descriptions also show in /mcps. They ship in English, and this
// test enforces it. It walks the embedded copies, which are what the
// daemon serves (tools/embed_test.go checks they match the files on disk).

var spanishChars = regexp.MustCompile(`[áéíóúñüÁÉÍÓÚÑÜ¿¡]`)

// Words common in Spanish prose that do not occur in English technical text. Short words that
// are also English, code or locale tags (el, la, es, de, y, solo, sin, del) stay out on purpose.
var spanishWords = regexp.MustCompile(`(?i)\b(que|para|los|las|una|con|por|pero|como|cuando|siempre|nunca|tarea|tareas|corrida|corridas|etapa|etapas|esta|este|esto|hay|puede|tiene|desde|hasta|sobre|entre|cada|porque|donde|otra|otro|nombre|rama|tablero|ejemplo)\b`)

// allowedSpanish lists "<path>:<match>" pairs that are legitimate (a quoted UI label, a proper
// noun). Keep it short; translate instead of adding to it.
var allowedSpanish = map[string]bool{}

func spanishHits(fsys fs.FS, prefix string) ([]string, int, error) {
	var hits []string
	files := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files++
		path := prefix + p
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for line := 1; sc.Scan(); line++ {
			text := sc.Text()
			matches := spanishChars.FindAllString(text, -1)
			matches = append(matches, spanishWords.FindAllString(text, -1)...)
			for _, m := range matches {
				if !allowedSpanish[path+":"+m] {
					hits = append(hits, path+":"+strconv.Itoa(line)+": "+strconv.Quote(m)+" in "+strconv.Quote(text))
				}
			}
		}
		return sc.Err()
	})
	return hits, files, err
}

func TestShippedPromptsAreEnglish(t *testing.T) {
	for _, src := range []struct {
		prefix string
		fsys   fs.FS
	}{
		{"internal/builtin/system-skills/", SystemSkills()},
		{"tools/", NativeTools()},
	} {
		hits, files, err := spanishHits(src.fsys, src.prefix)
		if err != nil {
			t.Fatalf("walk %s: %v", src.prefix, err)
		}
		if files == 0 {
			t.Fatalf("%s: no files walked; the guard is not looking at anything", src.prefix)
		}
		for _, h := range hits {
			t.Errorf("Spanish in a shipped prompt: %s", h)
		}
	}
}

func TestSpanishHitsCatchesSpanish(t *testing.T) {
	fsys := fstest.MapFS{
		"s/prompt.md":  {Data: []byte("# Run\n\nNunca cierres la tarea sin evidencia.\nUsa git status.\nAbrí el PR.\n")},
		"s/skill.yaml": {Data: []byte("description: Runs a task end to end.\n")},
	}
	hits, files, err := spanishHits(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Fatalf("walked %d files, want 2", files)
	}
	want := []string{
		`s/prompt.md:3: "Nunca" in "Nunca cierres la tarea sin evidencia."`,
		`s/prompt.md:3: "tarea" in "Nunca cierres la tarea sin evidencia."`,
		`s/prompt.md:5: "í" in "Abrí el PR."`,
	}
	if len(hits) != len(want) {
		t.Fatalf("hits = %q, want %q", hits, want)
	}
	for i := range want {
		if hits[i] != want[i] {
			t.Errorf("hit %d = %q, want %q", i, hits[i], want[i])
		}
	}
}
