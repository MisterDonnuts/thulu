package audit

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type sink struct {
	mu sync.Mutex
	w  io.Writer
}

// An unknown value is an error: an operator who set a sink and got none would believe the log was
// leaving the box.
func sinkFromEnv() (*sink, error) {
	raw := strings.TrimSpace(os.Getenv("OCTOPUS_AUDIT_SINK"))
	switch {
	case raw == "" || raw == "none" || raw == "off":
		return nil, nil
	case raw == "stdout":
		return &sink{w: os.Stdout}, nil
	case raw == "stderr":
		return &sink{w: os.Stderr}, nil
	case strings.HasPrefix(raw, "file:"):
		path := filepath.Clean(strings.TrimPrefix(raw, "file:"))
		if path == "." || path == "" {
			return nil, apperr.Invalid("audit.errors.sink_file_needs_path", "value", raw)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return nil, apperr.Wrap(err, "audit.errors.sink_open", "path", path)
		}
		return &sink{w: f}, nil
	}
	return nil, apperr.Invalid("audit.errors.sink_unknown", "value", raw)
}

type sinkRecord struct {
	Type string `json:"type"`
	Entry
}

// A failed write is logged and dropped: the DB row is already written, and audit must not break the
// request that caused it.
func (s *sink) write(e Entry) {
	if s == nil {
		return
	}
	line, err := json.Marshal(sinkRecord{Type: "octopus.audit", Entry: e})
	if err != nil {
		log.Printf("[audit] %s", i18n.Log("audit.log.sink_marshal_failed", "id", e.ID, "err", err))
		return
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(line); err != nil {
		log.Printf("[audit] %s", i18n.Log("audit.log.sink_write_failed", "id", e.ID, "err", err))
	}
}
