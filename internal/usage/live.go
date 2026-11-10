package usage

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/engine"

	// Registers the default engine so engine.MustGet("") resolves.
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
)

type LiveTotals struct {
	InputTokens    int
	OutputTokens   int
	CacheRead      int
	CacheCreation  int
	Turns          int
	LastPromptSize int
}

func (t LiveTotals) Total() int {
	return t.InputTokens + t.OutputTokens + t.CacheRead + t.CacheCreation
}

func LiveSnapshot(ctx context.Context, docker *client.Client, containerID, engineName string) LiveTotals {
	var t LiveTotals
	if docker == nil || containerID == "" {
		return t
	}
	reader, err := docker.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "all",
	})
	if err != nil {
		return t
	}
	defer reader.Close()

	return parseUsageStream(strings.NewReader(dockerx.DemuxAll(reader)), engineName)
}

func FileSnapshot(path, engineName string) LiveTotals {
	var t LiveTotals
	if path == "" {
		return t
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is a LogsFile written by the collector and persisted in the usage store, not external input
	if err != nil {
		return t
	}
	defer f.Close()
	return parseUsageStream(f, engineName)
}

// Claude Code prints one assistant line per content block, each repeating the message's usage:
// count each message id once.
func parseUsageStream(r io.Reader, engineName string) LiveTotals {
	var t LiveTotals
	eng := engine.MustGet(engineName)
	seen := map[string]bool{}
	for ev := range eng.ParseStream(r) {
		if ev.Kind != engine.EventAssistant || ev.Usage == nil {
			continue
		}
		if ev.MessageID != "" {
			if seen[ev.MessageID] {
				continue
			}
			seen[ev.MessageID] = true
		}
		u := ev.Usage
		t.InputTokens += u.InputTokens
		t.OutputTokens += u.OutputTokens
		t.CacheRead += u.CacheRead
		t.CacheCreation += u.CacheCreation
		t.Turns++
		t.LastPromptSize = u.PromptSize()
	}
	return t
}
