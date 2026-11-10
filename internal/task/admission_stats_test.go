package task

import (
	"context"
	"testing"
)

func TestAdmissionStatsReportsCapInflightAndWaiting(t *testing.T) {
	d := &Dispatcher{slots: newAgentSlots(nil), convSlots: newAgentSlots(nil)}
	d.WithAgentCap(func() int { return 1 })
	d.WithConversationCap(func() int { return -3 })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.slots.acquire(ctx, "ws-a"); err != nil {
		t.Fatal(err)
	}
	done := acquireAsync(d.slots, ctx, "ws-b")
	assertBlocked(t, done)

	stage, conv := d.AdmissionStats()
	if stage != (GateStats{Cap: 1, Inflight: 1, Waiting: 1}) {
		t.Errorf("stage gate = %+v, want cap 1, 1 running, 1 queued", stage)
	}
	if conv != (GateStats{}) {
		t.Errorf("conversation gate = %+v, want an unlimited idle gate (a negative cap reads as 0)", conv)
	}

	d.slots.release("ws-a")
	assertAdmitted(t, done)
	if stage, _ = d.AdmissionStats(); stage.Waiting != 0 || stage.Inflight != 1 {
		t.Errorf("after the hand-off = %+v, want 1 running, none queued", stage)
	}
}

func TestAdmissionStatsNilGates(t *testing.T) {
	stage, conv := (&Dispatcher{}).AdmissionStats()
	if stage != (GateStats{}) || conv != (GateStats{}) {
		t.Errorf("unwired gates = %+v %+v, want zero", stage, conv)
	}
}
