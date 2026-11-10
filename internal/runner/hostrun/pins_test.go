package hostrun

import (
	"os"
	"regexp"
	"testing"
)

func TestClaudeCodeVersionMatchesImage(t *testing.T) {
	b, err := os.ReadFile("../../../images/kube-agent/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ARG CLAUDE_CODE_VERSION=(\S+)$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("images/kube-agent/Dockerfile has no ARG CLAUDE_CODE_VERSION")
	}
	if got := string(m[1]); got != ClaudeCodeVersion {
		t.Errorf("the agent image installs Claude Code %s and a runner would get %s: bump ClaudeCodeVersion with the Dockerfile", got, ClaudeCodeVersion)
	}
}
