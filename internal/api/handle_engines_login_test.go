package api

import (
	"encoding/json"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

// The row proposes the engine's own login command, so an engine that gains a machine login needs no
// second definition in the web app.
func TestEnginesCarryTheMachineLoginCommand(t *testing.T) {
	t.Parallel()
	got := map[string]string{}
	for _, d := range availableEngines(t.Context()) {
		info := engineInfo(&d, nil, llmKeyStatusView{}, false, false)
		raw, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			Command string `json:"machine_login_command"`
		}
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		got[d.Name] = view.Command
		if (view.Command != "") != d.MachineLogin {
			t.Errorf("%s: machine_login=%v but command %q", d.Name, d.MachineLogin, view.Command)
		}
	}
	if got[engine.DefaultName] != "claude auth login" {
		t.Errorf("%s machine_login_command = %q, want %q", engine.DefaultName, got[engine.DefaultName], "claude auth login")
	}
}
