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
