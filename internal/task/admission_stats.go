package task

type GateStats struct {
	Cap      int
	Inflight int
	Waiting  int
}

func (a *agentSlots) snapshot() GateStats {
	if a == nil {
		return GateStats{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.cap()
	if c < 0 {
		c = 0
	}
	waiting := 0
	for _, q := range a.queues {
		waiting += len(q)
	}
	return GateStats{Cap: c, Inflight: a.inflight, Waiting: waiting}
}

func (d *Dispatcher) AdmissionStats() (stage, conversation GateStats) {
	return d.slots.snapshot(), d.convSlots.snapshot()
}
