package api

import (
	"testing"
	"time"
)

func TestAgentGitRefreshThrottle(t *testing.T) {
	var g agentGitState
	now := time.Now()
	if !g.refreshDue("p1", now) {
		t.Fatal("first fetch of a project must refresh")
	}
	if g.refreshDue("p1", now.Add(5*time.Second)) {
		t.Error("a fetch right after the last refresh must not refresh again")
	}
	if !g.refreshDue("p2", now.Add(5*time.Second)) {
		t.Error("another project is throttled on its own")
	}
	if !g.refreshDue("p1", now.Add(defaultRefreshEvery+time.Second)) {
		t.Error("a fetch after the window must refresh")
	}
}

func TestAgentGitPublishLockIsPerBranch(t *testing.T) {
	var g agentGitState
	if g.publishLock("root/p1") != g.publishLock("root/p1") {
		t.Error("the same branch must get the same lock")
	}
	if g.publishLock("root/p1") == g.publishLock("root/p2") {
		t.Error("different projects must not share a lock")
	}
}

// A fetch never waits on GitHub past refreshWait: the refresh goes on without it.
func TestWaitAtMostLetsASlowRefreshGoOnAlone(t *testing.T) {
	start := time.Now()
	if waitAtMost(20*time.Millisecond, make(chan struct{})) {
		t.Error("a refresh still running was reported finished")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("waited %s on a refresh that never ends", waited)
	}
	done := make(chan struct{})
	close(done)
	if !waitAtMost(time.Minute, done) {
		t.Error("a finished refresh was not reported")
	}
}
