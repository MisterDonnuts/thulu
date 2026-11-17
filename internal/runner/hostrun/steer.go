package hostrun

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// The queue steer-driver.js reads; the entrypoint's default STEER_QUEUE.
func steerDir(root string) string { return filepath.Join(root, ".steer") }

const steerQueueFile = "queue.jsonl"

// Same budget as steerAppendScript (internal/api/dockerlauncher.go): about 5 s for the driver's lock.
const (
	steerLockTries = 100
	steerLockWait  = 50 * time.Millisecond
	steerRetryWait = 2 * time.Second
)

// createSteerQueue runs before the daemon can hand the job a message, so the driver reads it from the
// first byte.
func createSteerQueue(root string) error {
	if err := os.MkdirAll(steerDir(root), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(steerDir(root), steerQueueFile), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// appendSteer is steerAppendScript in Go, under the lock steer-driver.js takes: a sealed queue
// (closed) means the agent stopped reading. It never creates the directory: a missing one is a job
// whose folder is gone, and recreating it would report a finished run as reached.
func appendSteer(root string, line []byte) string {
	dir := steerDir(root)
	lock := filepath.Join(dir, "lock")
	locked := false
	for i := 0; i < steerLockTries && !locked; i++ {
		err := os.Mkdir(lock, 0o700)
		switch {
		case err == nil:
			locked = true
		case errors.Is(err, fs.ErrNotExist):
			return runner.SteerSealed
		case !errors.Is(err, fs.ErrExist):
			return runner.SteerFailed
		default:
			time.Sleep(steerLockWait)
		}
	}
	if !locked {
		return runner.SteerLocked
	}
	defer func() { logx.Warn(os.Remove(lock), "hostrun.log.steer_unlock_failed", "path", lock) }()
	if _, err := os.Stat(filepath.Join(dir, "closed")); err == nil {
		return runner.SteerSealed
	}
	q := filepath.Join(dir, steerQueueFile)
	f, err := os.OpenFile(filepath.Clean(q), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return runner.SteerFailed
	}
	_, werr := f.Write(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return runner.SteerFailed
	}
	return runner.SteerAppended
}

// steerLoop carries the daemon's messages into the job's queue until ctx ends or the daemon says the
// job is over. The acks for one poll ride on the next.
func (j *job) steerLoop(ctx context.Context) {
	written := map[string]bool{}
	var acks []runner.SteerAck
	for ctx.Err() == nil {
		resp, err := j.r.client.Steer(ctx, j.spec.ID, acks)
		if err != nil {
			if isNotHeld(err) || ctx.Err() != nil {
				return
			}
			log.Printf("[runner] %s", i18n.Log("hostrun.log.steer_failed", "job", j.spec.ID, "err", err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(steerRetryWait):
			}
			continue
		}
		if resp.Done {
			return
		}
		acks = nil
		for _, m := range resp.Messages {
			acks = append(acks, runner.SteerAck{ID: m.ID, Result: j.appendOnce(written, m.Line)})
		}
	}
}

// The daemon may send the same message twice (a retried answer); the agent must read it once.
func (j *job) appendOnce(written map[string]bool, line string) string {
	id := payloadID(line)
	if id != "" && written[id] {
		return runner.SteerAppended
	}
	res := appendSteer(j.root, []byte(line))
	if res == runner.SteerAppended && id != "" {
		written[id] = true
	}
	return res
}

// A line that is not JSON has no id; the driver skips it anyway.
func payloadID(line string) string {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(line), &p); err != nil {
		return ""
	}
	return p.ID
}
