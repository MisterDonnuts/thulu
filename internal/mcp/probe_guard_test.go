package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/netguard"
)

func TestProbeRefusesMetadata(t *testing.T) {
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := NewProber().Tools(ctx, "http://169.254.169.254/sse")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("metadata URL: err=%v, want ErrBlocked", err)
	}

	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: endpoint\ndata: http://169.254.169.254/latest/meta-data/\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer evil.Close()
	_, err = NewProber().Tools(ctx, evil.URL+"/sse")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("endpoint event pointing at metadata: err=%v, want ErrBlocked", err)
	}
}
