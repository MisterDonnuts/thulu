package daemon

import (
	"testing"

	dtypes "github.com/docker/docker/api/types"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
)

func TestOrphanBrowsers(t *testing.T) {
	browser := func(id, owner string) dtypes.Container {
		labels := map[string]string{browserrun.LabelBrowser: "true"}
		if owner != "" {
			labels[browserrun.LabelOwner] = owner
		}
		return dtypes.Container{ID: id, Labels: labels}
	}
	running := map[string]bool{"agent-live": true}
	got := orphanBrowsers([]dtypes.Container{
		browser("b1", "agent-live"),
		browser("b2", "agent-gone"),
		browser("b3", ""),
	}, func(id string) bool { return running[id] })

	ids := map[string]bool{}
	for _, c := range got {
		ids[c.ID] = true
	}
	if ids["b1"] {
		t.Error("removed the browser of a running agent")
	}
	if !ids["b2"] {
		t.Error("kept the browser of an agent that is gone")
	}
	if !ids["b3"] {
		t.Error("kept a browser with no owner")
	}
}

func TestOrphanBrowsersSkipsAnotherInstall(t *testing.T) {
	dockerx.SetInstallID("mine")
	t.Cleanup(func() { dockerx.SetInstallID("") })
	theirs := dtypes.Container{ID: "b9", Labels: map[string]string{
		browserrun.LabelBrowser: "true", browserrun.LabelOwner: "their-agent", dockerx.InstallLabel: "theirs"}}
	if got := orphanBrowsers([]dtypes.Container{theirs}, func(string) bool { return false }); len(got) != 0 {
		t.Errorf("reaped another install's browser: %+v", got)
	}
}
