package exportbundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestExportLeavesOutWhoTriggeredARun(t *testing.T) {
	ctx := context.Background()
	k := buildStores(t, testpg.New(t), t.TempDir())
	must(t, k.task.Create(ctx, &task.Task{ID: "task_r", WorkspaceID: ws, BoardID: "custom", Title: "R"}), "create task")
	must(t, k.task.RecordEvent(ctx, "task_r", task.EventAgentRunStarted, "inst-1", "agent:dev", "build · dev",
		map[string]string{"instance_id": "inst-1", "llm_source": "personal_login"}), "record run")
	const person = "u_person_who_ran_it"
	if _, err := k.task.DB().ExecContext(ctx,
		`UPDATE workflow_events SET triggered_by = $1 WHERE task_id = 'task_r'`, person); err != nil {
		t.Fatal(err)
	}
	if got, err := k.task.RunTriggeredBy(ctx, "task_r", "inst-1"); err != nil || got != person {
		t.Fatalf("triggered_by = %q, %v: the column the test relies on is not where it reads", got, err)
	}

	b, attachments, err := exportbundle.Export(ctx, k.s, ws)
	must(t, err, "export")
	var out bytes.Buffer
	must(t, exportbundle.Write(&out, b, attachments), "write")
	r, _, err := exportbundle.Read(bytes.NewReader(out.Bytes()))
	must(t, err, "read")
	events := 0
	for _, ts := range r.Tasks {
		events += len(ts.Events)
	}
	if events == 0 {
		t.Fatal("the bundle carries no run event: the test proves nothing")
	}
	whole, err := json.Marshal(r)
	must(t, err, "marshal")
	if bytes.Contains(whole, []byte(person)) {
		t.Error("the bundle names the person who triggered the run")
	}
}
