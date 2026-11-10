package agentscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The real auditor.sh, engine-run.sh and adapter, with only the CLI faked: the agent ran on Claude
// Code and the auditor was set to another engine.
func TestAuditorRunsOnItsOwnEngineAndLeavesAVerdict(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root auditor.sh drops to the node user through gosu")
	}
	cases := []struct {
		engine, cli, fake, model string
		extraEnv                 []string
	}{
		{engine: "claude-code", cli: "claude", fake: fakeClaude, model: "haiku"},
		{engine: "codex", cli: "codex", fake: fakeCodex, model: "o3"},
		{engine: "opencode", cli: "opencode", fake: fakeOpenCode, model: "deepseek-chat", extraEnv: []string{
			"LLM_API_KEY=sk-secret-run-token", "OCTOPUS_LLM_PROVIDER=", "OCTOPUS_LLM_BASE_URL=https://api.deepseek.com/",
			"XDG_CONFIG_HOME=", "XDG_DATA_HOME=",
		}},
	}
	for _, c := range cases {
		t.Run(c.engine, func(t *testing.T) {
			switch c.engine {
			case "codex":
				needBash4(t)
			case "opencode":
				needNode(t)
			}
			const verdict = "---\nverdict: approve\n---\nok"
			fake := strings.Replace(c.fake, "#!/bin/sh\n",
				"#!/bin/sh\nprintf '%s' \"$FAKE_VERDICT\" >\"$OUTPUT_DIR/feedback.md\"\n", 1)
			r := newAdapterRun(t, c.cli, fake)

			root := t.TempDir()
			if err := os.Symlink(snippetsDir(t), filepath.Join(root, "snippets")); err != nil {
				t.Fatal(err)
			}
			prompts := filepath.Join(root, "auditor")
			writeFile(t, filepath.Join(prompts, "done.md"), "AUDIT THE WORK\n")
			output := filepath.Join(root, "output")
			if err := os.MkdirAll(output, 0o755); err != nil {
				t.Fatal(err)
			}

			script := "set +e; source " + filepath.Join(snippetsDir(t), "i18n.sh") +
				"; source " + filepath.Join(snippetsDir(t), "auditor.sh")
			cmd := exec.Command(bashOrSkip(t), "-c", script)
			cmd.Dir = r.wd
			cmd.Env = append(append([]string{}, r.env...), c.extraEnv...)
			cmd.Env = append(cmd.Env,
				"OCTOPUS_ROOT="+root, "OCTOPUS_TMP="+root, "OUTPUT_DIR="+output, "STAGE=done",
				"AGENT_NAME=developer", "AUDITED_AGENT=developer", "AUDITED_INSTANCE=developer-1",
				"ENGINE=claude-code", "MODEL=sonnet", "FAKE_VERDICT="+verdict,
				"AUDITOR_ENGINE="+c.engine, "AUDITOR_MODEL="+c.model, "AUDITOR_TURNS=3",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("auditor.sh: %v\n%s", err, out)
			}

			if n := r.calls(t); n < 1 {
				t.Fatalf("the %s CLI never ran:\n%s", c.cli, out)
			}
			args, stdin := r.call(t, 1)
			if !strings.Contains(stdin, "AUDIT THE WORK") && !strings.Contains(strings.Join(args, " "), "AUDIT THE WORK") {
				t.Errorf("the prompt did not reach the CLI: args %v stdin %q", args, stdin)
			}
			if !strings.Contains(strings.Join(args, " "), c.model) {
				t.Errorf("args %v do not carry the auditor's model %q", args, c.model)
			}
			got, err := os.ReadFile(filepath.Join(output, "feedback.md"))
			if err != nil || string(got) != verdict {
				t.Errorf("feedback.md = %q, %v\n%s", got, err, out)
			}
		})
	}
}
