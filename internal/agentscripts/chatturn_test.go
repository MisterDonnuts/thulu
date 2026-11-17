package agentscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// chatTurnRoot lays out chat-turn.sh next to the real i18n.sh and an engine-run.sh that prints the
// HOME it got and the message it read.
func chatTurnRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"chat-turn.sh", "i18n.sh"} {
		b, err := os.ReadFile(filepath.Join(snippetsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "snippets", name), string(b))
	}
	writeFile(t, filepath.Join(root, "snippets", "engine-run.sh"), `#!/usr/bin/env bash
printf '{"home":"%s","message":"%s"}\n' "$HOME" "$(tail -n 1)"
`)
	if err := os.Chmod(filepath.Join(root, "snippets", "engine-run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// Case 45: on a machine the turn keeps the person's HOME, where the engine's login lives; a
// container still points it at node's.
func TestChatTurnKeepsTheMachinesHome(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("as root the container path runs gosu")
	}
	home := t.TempDir()
	for _, c := range []struct {
		name     string
		hostRun  string
		wantHome string
	}{
		{"machine", "1", home},
		{"container", "", "/home/node"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := chatTurnRoot(t)
			cmd := exec.Command(bashOrSkip(t), filepath.Join(root, "snippets", "chat-turn.sh"))
			cmd.Stdin = strings.NewReader("hola")
			cmd.Env = append(os.Environ(), "OCTOPUS_ROOT="+root, "OCTOPUS_HOST_RUN="+c.hostRun, "HOME="+home,
				"CHAT_ID=chat-1", "SESSION_ID=s1", "OCTOPUS_LOCALE=en")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("chat-turn.sh: %v\n%s", err, out)
			}
			if want := `{"home":"` + c.wantHome + `","message":"hola"}`; strings.TrimSpace(string(out)) != want {
				t.Errorf("turn printed %q, want %q", out, want)
			}
		})
	}
}

// chatModeBlock is the entrypoint's chat mode, from its if to the matching fi.
func chatModeBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(snippetsDir(t)), "entrypoint-base.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "if [ \"${STAGE:-}\" = \"chat\" ]; then\n  MCP_CONFIG_ARGS=")
	if start < 0 {
		t.Fatal("entrypoint-base.sh has no chat mode")
	}
	end := strings.Index(src[start:], "\nfi\n")
	if end < 0 {
		t.Fatal("the chat mode has no closing fi")
	}
	return src[start : start+end+len("\nfi\n")]
}

// On a machine, chat mode runs the turn with the message the runner wrote instead of waiting for
// an exec that never comes.
func TestEntrypointChatModeRunsTheTurnOnAMachine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "snippets", "chat-turn.sh"), "#!/usr/bin/env bash\necho \"turn read: $(cat)\"\n")
	if err := os.Chmod(filepath.Join(root, "snippets", "chat-turn.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "tmp", "job.message")
	writeFile(t, input, "hola")
	block := "msg_en() { :; }\n_octopus_host_run=1\n" + chatModeBlock(t) + "echo parked\n"
	cmd := exec.Command(bashOrSkip(t), "-c", block)
	cmd.Env = append(os.Environ(), "STAGE=chat", "OCTOPUS_ROOT="+root, "CHAT_INPUT_FILE="+input, "MCP_TOOLS=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "turn read: hola") || strings.Contains(string(out), "parked") {
		t.Errorf("output = %q, want the turn to read the runner's message and the script to end there", out)
	}
}

// Case 45 through the whole chain on a machine: the entrypoint's chat mode hands the real
// chat-turn.sh the message the runner wrote, and the engine reads it with the person's HOME.
func TestEntrypointRunsTheRealChatTurnOnAMachine(t *testing.T) {
	root := chatTurnRoot(t)
	if err := os.Chmod(filepath.Join(root, "snippets", "chat-turn.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	input := filepath.Join(root, "tmp", "job.message")
	writeFile(t, input, "hola")
	block := "msg_en() { :; }\n_octopus_host_run=1\n" + chatModeBlock(t) + "echo parked\n"
	cmd := exec.Command(bashOrSkip(t), "-c", block)
	cmd.Env = append(os.Environ(), "STAGE=chat", "OCTOPUS_ROOT="+root, "CHAT_INPUT_FILE="+input, "MCP_TOOLS=",
		"OCTOPUS_HOST_RUN=1", "HOME="+home, "CHAT_ID=chat-1", "SESSION_ID=s1", "OCTOPUS_LOCALE=en")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := `{"home":"` + home + `","message":"hola"}`; !strings.Contains(string(out), want) ||
		strings.Contains(string(out), "parked") {
		t.Errorf("output = %q, want %s and the script to end with the turn", out, want)
	}
}
