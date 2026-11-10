package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const sseHeartbeat = 15 * time.Second

func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func writeSSELine(w io.Writer, line string) bool {
	for _, sub := range strings.Split(line, "\n") {
		//i18n:ignore SSE wire format field, not a message
		if _, err := fmt.Fprintf(w, "data: %s\n", sub); err != nil {
			return false
		}
	}
	_, err := fmt.Fprint(w, "\n")
	return err == nil
}

// The heartbeat writes from its own goroutine; without the mutex frames interleave and the client
// cannot parse them.
type sseConn struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
	broken  bool
}

func newSSEConn(w http.ResponseWriter, r *http.Request) *sseConn {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrKey(w, r, http.StatusInternalServerError, "api.sse.errors.streaming_unsupported")
		return nil
	}
	setSSEHeaders(w)
	w.WriteHeader(http.StatusOK)
	return &sseConn{w: w, flusher: flusher}
}

func (c *sseConn) write(fn func() bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return false
	}
	if !fn() {
		c.broken = true
		return false
	}
	c.flusher.Flush()
	return true
}

func (c *sseConn) line(s string) bool {
	return c.write(func() bool { return writeSSELine(c.w, s) })
}

func (c *sseConn) event(typ string, data []byte) bool {
	return c.write(func() bool {
		//i18n:ignore SSE wire format fields, not a message
		_, err := fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", typ, data)
		return err == nil
	})
}

func (c *sseConn) events(batch []sseFrame) bool {
	if len(batch) == 0 {
		return true
	}
	return c.write(func() bool {
		for _, f := range batch {
			//i18n:ignore SSE wire format fields, not a message
			if _, err := fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", f.Type, f.Data); err != nil {
				return false
			}
		}
		return true
	})
}

type sseFrame struct {
	Type string
	Data []byte
}

func (c *sseConn) comment(s string) bool {
	return c.write(func() bool {
		_, err := fmt.Fprintf(c.w, ": %s\n\n", s)
		return err == nil
	})
}

func (c *sseConn) heartbeat(ctx context.Context) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(sseHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if !c.comment("ping") {
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
