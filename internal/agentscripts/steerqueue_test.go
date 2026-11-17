package agentscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// steerQueueBlock is the entrypoint's queue setup, from the STEER_QUEUE line to the end of its if.
func steerQueueBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(snippetsDir(t)), "entrypoint-base.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, `STEER_QUEUE="${STEER_QUEUE:-`)
	if start < 0 {
		t.Fatal("entrypoint-base.sh no longer sets STEER_QUEUE")
	}
	end := strings.Index(src[start:], "\nfi\n")
	if end < 0 {
		t.Fatal("the STEER_QUEUE block has no closing fi")
	}
	return src[start : start+end+len("\nfi\n")]
}

// Case 36: a message that lands before the engine starts stays in the queue, and a missing queue is
// created empty.
func TestEntrypointCreatesTheSteerQueueWithoutTruncating(t *testing.T) {
	block := "msg_en() { :; }\n" + steerQueueBlock(t)
	for _, c := range []struct {
		name   string
		before string
	}{
		{"missing", ""},
		{"early message", `{"id":"m1","text":"hi"}` + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			queue := filepath.Join(root, ".steer", "queue.jsonl")
			if c.before != "" {
				writeFile(t, queue, c.before)
			}
			cmd := exec.Command(bashOrSkip(t), "-c", block)
			cmd.Env = append(cmd.Environ(), "STEER_ENABLED=1", "OCTOPUS_ROOT="+root, "STEER_QUEUE=")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			b, err := os.ReadFile(queue)
			if err != nil || string(b) != c.before {
				t.Errorf("queue = %q, %v; want %q", b, err, c.before)
			}
		})
	}
}
