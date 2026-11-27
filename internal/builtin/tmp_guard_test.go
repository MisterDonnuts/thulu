package builtin

import (
	"bufio"
	"bytes"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// On a runner the agent runs on a person's own machine: /tmp there is the machine's shared temp
// folder, which nothing cleans, and a process detached with `setsid nohup` outlives the run. A run
// once copied the client's repo to /tmp and left `ng serve` running there for nine hours, until it
// took the whole machine's memory. Prompts send scratch files to "$TMPDIR" (the run's own folder on
// a runner, /tmp in a container) and long-lived servers to init_run.

// A /tmp path as a prompt writes it: at the start of a line or a word, in backticks, quotes or
// parentheses. "${TMPDIR:-/tmp}" (the sandbox fallback) and "/private/tmp" do not match.
var literalTmp = regexp.MustCompile("(?:^|[\\s`\"'(=])/tmp\\b")

const detachAdvice = "setsid nohup"

// allowedTmp lists "<path>:<line text fragment>" entries that may keep a literal /tmp, each with
// the reason. Rewrite the prompt instead of adding to it.
var allowedTmp = map[string]string{
	// Describes where init_run itself keeps its copy of the script inside the sandbox container;
	// the agent reads it there, it never writes to it.
	"tools/thulu-sandbox/skill.md:Copies the script to `/tmp/octopus-init/<dir>/run.sh`": "init_run's own path inside the sandbox container",
	"tools/thulu-sandbox/skill.md:**Startup logs**: `/tmp/octopus-init/<dir>.log`":       "init_run's log inside the sandbox container",
}

func tmpHits(fsys fs.FS, prefix string, keep func(string) bool) ([]string, int, map[string]bool, error) {
	var hits []string
	used := map[string]bool{}
	files := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if keep != nil && !keep(p) {
			return nil
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
			if !literalTmp.MatchString(text) && !strings.Contains(text, detachAdvice) {
				continue
			}
			if key := allowedKey(path, text); key != "" {
				used[key] = true
				continue
			}
			hits = append(hits, path+":"+strconv.Itoa(line)+": "+strconv.Quote(text))
		}
		return sc.Err()
	})
	return hits, files, used, err
}

func allowedKey(path, text string) string {
	for key := range allowedTmp {
		p, frag, _ := strings.Cut(key, ":")
		if p == path && strings.Contains(text, frag) {
			return key
		}
	}
	return ""
}

func TestPromptsKeepScratchFilesInTheRun(t *testing.T) {
	isSkillManual := func(p string) bool { return strings.HasSuffix(p, "/skill.md") }
	used := map[string]bool{}
	for _, src := range []struct {
		prefix string
		fsys   fs.FS
		keep   func(string) bool
	}{
		{"internal/builtin/system-skills/", SystemSkills(), nil},
		{"internal/builtin/workshop/", Workshop(), nil},
		{"tools/", NativeTools(), isSkillManual},
	} {
		hits, files, u, err := tmpHits(src.fsys, src.prefix, src.keep)
		if err != nil {
			t.Fatalf("walk %s: %v", src.prefix, err)
		}
		if files == 0 {
			t.Fatalf("%s: no files walked; the guard is not looking at anything", src.prefix)
		}
		for k := range u {
			used[k] = true
		}
		for _, h := range hits {
			t.Errorf("%s\n\twrite \"$TMPDIR\" (or \"${TMPDIR:-/tmp}\" for a command run in the sandbox) instead of /tmp, "+
				"and send long-lived servers to init_run instead of setsid nohup", h)
		}
	}
	for key, reason := range allowedTmp {
		if !used[key] {
			t.Errorf("allowedTmp entry %q (%s) matches nothing any more; remove it", key, reason)
		}
	}
}

func TestTmpGuardCatchesTheShapesPromptsUse(t *testing.T) {
	for text, want := range map[string]bool{
		"Scratch files go to `/tmp`.":                       true,
		"cp -a /workspace/projects/x /tmp/repro-1":          true,
		"--out /tmp/thulu-gates/dev":                        true,
		"git diff main > /tmp/diff.txt":                     true,
		"--out \"${TMPDIR:-/tmp}/thulu-gates/dev\"":         false,
		"lives in `$TMPDIR`":                                false,
		"macOS resolves it under /private/tmp on the host.": false,
	} {
		if got := literalTmp.MatchString(text); got != want {
			t.Errorf("literalTmp.MatchString(%q) = %v, want %v", text, got, want)
		}
	}
}
