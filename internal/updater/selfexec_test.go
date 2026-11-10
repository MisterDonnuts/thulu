package updater

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExecBinaryInterruptsAndWaits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals")
	}
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	done := filepath.Join(dir, "backed-out")
	script := filepath.Join(dir, "octopus")
	body := "#!/bin/sh\n" +
		"trap 'sleep 1; echo ok > \"" + done + "\"; exit 3' INT\n" +
		"touch \"" + ready + "\"\n" +
		"while :; do sleep 0.1; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	u := &updateRun{in: &Install{Root: dir}, deps: &Deps{}}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- u.execBinary(ctx, script, nil) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("want the child's exit status as an error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("execBinary did not return")
	}
	if _, err := os.Stat(done); err != nil {
		t.Fatalf("the child was not allowed to back out: %v", err)
	}
}
