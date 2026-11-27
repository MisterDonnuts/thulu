package hostrun

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

// helperEnv turns the test binary into a process a fake agent leaves behind: it is not a platform
// binary, so macOS shows its environment the way it shows node's.
const helperEnv = "THULU_TEST_HELPER"

func TestMain(m *testing.M) {
	mode := os.Getenv(helperEnv)
	if mode != "" && mode != helperRunner {
		runHelper(mode)
		return
	}
	if err := testgit.IsolateHostConfig(); err != nil {
		panic(err)
	}
	// This machine's Claude Code policy and MCPs stay out of the tests: each test that needs them
	// sets its own.
	managedSettingsDir = func() string { return "" }
	managedPlists = func() []string { return nil }
	userSettingsPath = func() string { return "" }
	listMCPs = func(context.Context, []string, string) ([]ListedMCP, error) { return nil, nil }
	// The machine's /tmp and Docker stay out of the tests: a run's end scans both. Runner
	// subprocesses share the parent's folder through the environment.
	dockerCmd = "thulu-test-no-docker"
	if dir := os.Getenv(testTmpRootEnv); dir != "" {
		tmpRoot = dir
	} else {
		dir, err := os.MkdirTemp("", "thulu-tmproot-")
		if err != nil {
			panic(err)
		}
		tmpRoot = dir
		_ = os.Setenv(testTmpRootEnv, dir)
	}
	if mode == helperRunner {
		os.Exit(runRunner())
	}
	code := m.Run()
	_ = os.RemoveAll(os.Getenv(testTmpRootEnv))
	os.Exit(code)
}

// The folder a test binary uses as the machine's /tmp.
const testTmpRootEnv = "THULU_TEST_TMPROOT"

// helperRunner turns the test binary into `thulu runner run`, for the tests that crash a runner or
// signal it.
const helperRunner = "runner"

// runRunner runs a whole runner against the daemon at THULU_TEST_DAEMON, in the work dir
// THULU_TEST_WORK, with THULU_TEST_ENTRY as every run's script, until a stop signal.
func runRunner() int {
	cfg := &Config{DaemonURL: os.Getenv("THULU_TEST_DAEMON"), WorkDir: os.Getenv("THULU_TEST_WORK"),
		Entrypoint: os.Getenv("THULU_TEST_ENTRY"), Concurrency: 1, ChatConcurrency: 1,
		// The machine running the tests may be short of memory; these tests are about other things.
		MinFreeMemoryMB: -1, MachineMemoryFloorMB: -1}
	ctx, stop := signal.NotifyContext(context.Background(), StopSignals...)
	defer stop()
	if err := New(cfg, "test").Run(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// runHelper sleeps for ten minutes: sleep as it is, ignore-term deaf to SIGTERM, alloc after it
// takes THULU_TEST_ALLOC_MB of memory (64 by default).
func runHelper(mode string) {
	switch mode {
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
	case "alloc":
		mb, err := strconv.Atoi(os.Getenv("THULU_TEST_ALLOC_MB"))
		if err != nil || mb <= 0 {
			mb = 64
		}
		buf := make([]byte, mb<<20)
		for i := range buf {
			if i%4096 == 0 {
				buf[i] = 1
			}
		}
		defer func() { _ = buf[len(buf)-1] }()
	}
	time.Sleep(10 * time.Minute)
}
