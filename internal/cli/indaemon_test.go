package cli

import (
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

func TestDaemonExecRunsTheSubcommandInTheDaemonContainer(t *testing.T) {
	args := []string{"export", "-workspace", "acme"}
	cases := []struct {
		in       updater.Install
		wantName string
		wantArgv []string
	}{
		{
			in:       updater.Install{Mode: initwizard.ModeDocker, ComposeFile: "deploy/docker-compose.yml"},
			wantName: "docker",
			wantArgv: []string{"compose", "-f", "deploy/docker-compose.yml", "exec", "-T", "octopus", "./octopus",
				"export", "-workspace", "acme"},
		},
		{
			in:       updater.Install{Mode: initwizard.ModeKubernetes, Namespace: "ops", Release: "octopus"},
			wantName: "kubectl",
			wantArgv: []string{"exec", "-i", "-n", "ops", "deploy/octopus-octopus", "--", "/app/octopus",
				"export", "-workspace", "acme"},
		},
	}
	for _, c := range cases {
		name, argv := daemonExec(&c.in, args)
		if name != c.wantName || !slices.Equal(argv, c.wantArgv) {
			t.Errorf("%s mode: got %s %q, want %s %q", c.in.Mode, name, argv, c.wantName, c.wantArgv)
		}
	}
}

func TestImportArgsReadTheBundleFromStdin(t *testing.T) {
	cases := []struct {
		opts ImportOptions
		want []string
	}{
		{ImportOptions{}, []string{"import", "-"}},
		{
			ImportOptions{Workspace: "acme", WithSettings: true, JSON: true},
			[]string{"import", "-workspace", "acme", "-with-settings", "-json", "-"},
		},
	}
	for _, c := range cases {
		if got := importArgs(c.opts); !slices.Equal(got, c.want) {
			t.Errorf("importArgs(%+v) = %q, want %q", c.opts, got, c.want)
		}
	}
}
