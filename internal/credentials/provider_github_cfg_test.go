package credentials

import "testing"

func TestGithubAPIBaseFollowsConfig(t *testing.T) {
	old := githubAPI
	t.Cleanup(func() { githubAPI = old })
	githubAPI = ""
	t.Setenv("OCTOPUS_GITHUB_URL", "https://ghe.example.com")
	t.Setenv("OCTOPUS_GITHUB_API_URL", "")
	if got := githubAPIBase(); got != "https://ghe.example.com/api/v3" {
		t.Errorf("githubAPIBase = %q", got)
	}
	t.Setenv("OCTOPUS_GITHUB_URL", "")
	if got := githubAPIBase(); got != "https://api.github.com" {
		t.Errorf("githubAPIBase default = %q", got)
	}
}
