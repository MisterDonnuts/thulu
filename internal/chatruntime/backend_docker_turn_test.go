package chatruntime

import (
	"bufio"
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// discardConn takes the turn's stdin and tells the fake when the backend hangs up.
type discardConn struct {
	net.Conn
	closeOnce sync.Once
	onClose   func()
}

func (c *discardConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *discardConn) CloseWrite() error           { return nil }
func (c *discardConn) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

// turnDocker records every exec. The turn's exec hangs until the backend hangs up; every other exec
// ends at once with no output.
type turnDocker struct {
	listOnlyDocker
	mu    sync.Mutex
	execs []container.ExecOptions
}

func (d *turnDocker) ContainerExecCreate(_ context.Context, _ string, o container.ExecOptions) (dockertypes.IDResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.execs = append(d.execs, o)
	return dockertypes.IDResponse{ID: strings.Join(o.Cmd[:1], "")}, nil
}

func (d *turnDocker) ContainerExecAttach(_ context.Context, id string, _ container.ExecStartOptions) (dockertypes.HijackedResponse, error) {
	if id != "setsid" {
		return dockertypes.HijackedResponse{Conn: &discardConn{}, Reader: bufio.NewReader(strings.NewReader(""))}, nil
	}
	pr, pw := io.Pipe()
	conn := &discardConn{onClose: func() { pw.CloseWithError(io.EOF) }}
	return dockertypes.HijackedResponse{Conn: conn, Reader: bufio.NewReader(pr)}, nil
}

func (d *turnDocker) ContainerExecInspect(context.Context, string) (container.ExecInspect, error) {
	return container.ExecInspect{}, nil
}

func (d *turnDocker) snapshot() []container.ExecOptions {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.execs)
}

func TestDockerChatTurnStoppedKillsItsGroupAndReapsWhatItLeft(t *testing.T) {
	d := &turnDocker{}
	b := &dockerBackend{docker: d}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if _, err := b.Exec(ctx, ExecSpec{ContainerID: "ctr", Env: []string{"SESSION_ID=s"}, Stdin: "hi"}); err == nil {
		t.Fatal("a turn past its deadline came back without an error")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(d.snapshot()) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	execs := d.snapshot()
	if len(execs) != 3 {
		t.Fatalf("want the turn, the group kill and the reap; got %d execs: %+v", len(execs), execs)
	}

	turn := execs[0]
	if got := turn.Cmd[:3]; !slices.Equal(got, []string{"setsid", "-w", "sh"}) {
		t.Fatalf("the turn does not run in its own session: %q", turn.Cmd)
	}
	if !strings.Contains(turn.Cmd[4], "timeout --kill-after=") || turn.Cmd[5] != "1" || turn.Cmd[6] != chatTurnPgidFile {
		t.Fatalf("the turn is not bounded by its deadline: %q", turn.Cmd)
	}
	var marker string
	for _, kv := range turn.Env {
		if v, ok := strings.CutPrefix(kv, envChatTurn+"="); ok {
			marker = v
		}
	}
	if marker == "" || !slices.Contains(turn.Env, "SESSION_ID=s") {
		t.Fatalf("the turn's env lost the session or the marker: %q", turn.Env)
	}

	kill := execs[1]
	if !strings.Contains(strings.Join(kill.Cmd, " "), "kill -KILL") || !slices.Contains(kill.Cmd, chatTurnPgidFile) {
		t.Fatalf("the stopped turn's group was not killed: %q", kill.Cmd)
	}
	reap := execs[2]
	if reap.Cmd[0] != "bash" || !slices.Contains(reap.Env, "OCTOPUS_REAP_VALUE="+marker) || !slices.Contains(reap.Env, "OCTOPUS_REAP_VAR="+envChatTurn) {
		t.Fatalf("the reap does not target this turn's marker %q: %q %q", marker, reap.Cmd[:1], reap.Env)
	}
}

func TestTurnSecondsFollowsTheDeadline(t *testing.T) {
	if got := turnSeconds(context.Background()); got != chatTurnDefaultSeconds {
		t.Fatalf("no deadline: %d", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if got := turnSeconds(ctx); got < 89 || got > 90 {
		t.Fatalf("90 s deadline gave %d", got)
	}
}
