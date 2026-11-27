package hostrun

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/logx"
)

const logShipEvery = time.Second

// Stays under the daemon's 4 MB per log call.
const maxLogBuffer = 2 << 20

// How long one log call may take, and the last one when the runner is on its way out.
const (
	logShipTimeout      = 30 * time.Second
	logShipQuickTimeout = 5 * time.Second
)

type logShipper struct {
	ctx    context.Context
	client *Client
	jobID  string

	mu      sync.Mutex
	buf     bytes.Buffer
	timeout time.Duration
	stop    chan struct{}
	done    chan struct{}
}

func newLogShipper(ctx context.Context, c *Client, jobID string) *logShipper {
	s := &logShipper{ctx: ctx, client: c, jobID: jobID, timeout: logShipTimeout, stop: make(chan struct{}), done: make(chan struct{})}
	go s.loop()
	return s
}

func (s *logShipper) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.Len()+len(p) > maxLogBuffer {
		s.flushLocked()
	}
	s.buf.Write(p)
	return len(p), nil
}

func (s *logShipper) loop() {
	defer close(s.done)
	t := time.NewTicker(logShipEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.mu.Lock()
			s.flushLocked()
			s.mu.Unlock()
			return
		case <-t.C:
			s.mu.Lock()
			s.flushLocked()
			s.mu.Unlock()
		}
	}
}

func (s *logShipper) flushLocked() {
	if s.buf.Len() == 0 {
		return
	}
	chunk := append([]byte(nil), s.buf.Bytes()...)
	s.buf.Reset()
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	defer cancel()
	// A failed chunk is dropped, not retried: the run matters more than its log.
	logx.Warn(s.client.Log(ctx, s.jobID, chunk), "hostrun.log.ship_failed", "job", s.jobID, "bytes", len(chunk))
}

// close sends what is left; quick gives that last call logShipQuickTimeout.
func (s *logShipper) close(quick bool) {
	if quick {
		s.mu.Lock()
		s.timeout = logShipQuickTimeout
		s.mu.Unlock()
	}
	close(s.stop)
	<-s.done
}
