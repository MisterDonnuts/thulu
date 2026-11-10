package personallogin

import (
	"strings"
	"testing"
)

// What `claude auth login` (Claude Code 2.1) prints in the agent image: the URL in an OSC 8
// hyperlink, then as colored text.
const claudeLoginTerm = "Opening browser to sign in…\r\nIf the browser didn't open, visit: " +
	"\x1b]8;;" + claudeURL + "\x07\x1b[94m" + claudeURL + "\x1b[39m\x1b]8;;\x07\r\nPaste code here if prompted > "

const claudeURL = "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e" +
	"&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback" +
	"&scope=user%3Ainference&code_challenge=abc&code_challenge_method=S256&state=xyz"

func TestAuthorizationURL(t *testing.T) {
	if got := AuthorizationURL([]byte(claudeLoginTerm)); got != claudeURL {
		t.Errorf("url =\n%s\nwant\n%s", got, claudeURL)
	}
	if got := AuthorizationURL([]byte(claudeLoginTerm[:strings.Index(claudeLoginTerm, "&state")])); got != "" {
		t.Errorf("partial output gave %q", got)
	}
	for _, out := range []string{
		"Welcome! Docs at https://docs.claude.com/en/docs\r\n",
		"visit http://claude.com/cai/oauth/authorize?state=x\r\n",
		"",
	} {
		if got := AuthorizationURL([]byte(out)); got != "" {
			t.Errorf("%q gave %q", out, got)
		}
	}
}

func TestVolumeName(t *testing.T) {
	if got := VolumeName("claude-code", "u_1a2b"); got != "octopus-login-claude-code-u_1a2b" {
		t.Errorf("name = %q", got)
	}
	a, b := VolumeName("claude-code", "a@b"), VolumeName("claude-code", "a#b")
	if a == b || strings.ContainsAny(a, "@#") {
		t.Errorf("unsafe ids gave %q and %q", a, b)
	}
}
