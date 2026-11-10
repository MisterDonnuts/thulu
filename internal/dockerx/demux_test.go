package dockerx_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"

	"github.com/MisterDonnuts/thulu/internal/dockerx"
)

func mux(t *testing.T, frames ...string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	for i, f := range frames {
		w := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
		if i%2 == 1 {
			w = stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
		}
		if _, err := w.Write([]byte(f)); err != nil {
			t.Fatalf("mux write: %v", err)
		}
	}
	return &buf
}

func TestDemuxAll_StripsHeaders(t *testing.T) {
	in := mux(t, "hello ", "wor", "ld\n")
	if got, want := dockerx.DemuxAll(in), "hello world\n"; got != want {
		t.Errorf("DemuxAll = %q, want %q", got, want)
	}
}

func TestDemuxAll_TornFinalFrame(t *testing.T) {
	full := mux(t, "complete", "torn-frame-payload").Bytes()
	in := bytes.NewReader(full[:len(full)-8])
	got := dockerx.DemuxAll(in)
	if !strings.HasPrefix(got, "complete") {
		t.Errorf("DemuxAll on torn stream = %q, want the complete frames preserved", got)
	}
}

func TestDemux_PassthroughPlainText(t *testing.T) {
	plain := "2026-07-17 plain log line\nanother line\n"
	var out bytes.Buffer
	if err := dockerx.Demux(&out, strings.NewReader(plain)); err != nil {
		t.Fatalf("Demux: %v", err)
	}
	if out.String() != plain {
		t.Errorf("Demux passthrough = %q, want %q", out.String(), plain)
	}
}

func TestDemux_PassthroughShortStream(t *testing.T) {
	short := "hi\n"
	var out bytes.Buffer
	if err := dockerx.Demux(&out, strings.NewReader(short)); err != nil {
		t.Fatalf("Demux: %v", err)
	}
	if out.String() != short {
		t.Errorf("Demux short passthrough = %q, want %q", out.String(), short)
	}
}

type countingWriter struct{ writes []string }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes = append(c.writes, string(p))
	return len(p), nil
}

func TestDemux_OneWritePerFrame(t *testing.T) {
	in := mux(t, "frame-1\n", "frame-2\n", "frame-3\n")
	var cw countingWriter
	if err := dockerx.Demux(&cw, in); err != nil {
		t.Fatalf("Demux: %v", err)
	}
	want := []string{"frame-1\n", "frame-2\n", "frame-3\n"}
	if len(cw.writes) != len(want) {
		t.Fatalf("writes = %q, want %q (one Write per frame)", cw.writes, want)
	}
	for i := range want {
		if cw.writes[i] != want[i] {
			t.Errorf("write %d = %q, want %q", i, cw.writes[i], want[i])
		}
	}
}
