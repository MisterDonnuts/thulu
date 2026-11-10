package agentscripts

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/tokenopt"
)

func TestDelegateCLIUserMessageMatchesDaemon(t *testing.T) {
	node := nodeOrSkip(t)
	reqs := []tokenopt.Request{
		{Kind: tokenopt.KindRead, Question: "What does A do?", Files: []tokenopt.File{
			{Path: "a.go", Content: "package a\n"},
			{Path: `we<i>rd "&" name.go`, Content: "no newline"},
		}},
		{Kind: tokenopt.KindRead, Question: "One file", Files: []tokenopt.File{{Path: "b.go", Content: ""}}},
	}
	for _, req := range reqs {
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(node, "-e",
			`process.stdout.write(require(process.argv[1]).userMessage(JSON.parse(process.argv[2])))`,
			snippet(t, "octopus-delegate.js"), string(body)).Output()
		if err != nil {
			t.Fatalf("node: %v", err)
		}
		if want := tokenopt.UserMessage(req); string(out) != want {
			t.Errorf("%s message differs\n cli: %q\nwant: %q", req.Kind, out, want)
		}
	}
}
