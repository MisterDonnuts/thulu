package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

var errNoLogs error = apperr.NotFound("api.logs.errors.no_logs")

// Needs its own key: apperr matches by key, and sharing errNoLogs's would make every missing run
// wait forever.
var errLogsPending error = apperr.NotFound("api.logs.errors.not_started")

type lineSink func(string) bool

type logSource struct {
	path   string
	pod    *podTail
	docker io.ReadCloser
}

// A following log stream can end while the pod still runs (API server or kubelet restart, idle
// proxy cut).
// On EOF, ask the pod and reopen from the last emitted line if the agent is still running.
type podTail struct {
	reader io.ReadCloser
	state  func(context.Context) (kubeapi.LogReadiness, error)
	reopen func(ctx context.Context, since time.Time) (io.ReadCloser, error)
}

func (s *Server) resolveSessionLogs(ctx context.Context, id string) (*logSource, error) {
	if path := s.sessionLogPath(ctx, id); path != "" {
		return &logSource{path: path}, nil
	}
	// Pending, not missing: a queued runner run has no log file until its first chunk, and pending
	// keeps the wait alive.
	if runner.IsRunnerRun(id) {
		return nil, errLogsPending
	}
	if s.kube != nil {
		return s.resolvePodLogs(ctx, id)
	}
	if s.docker == nil {
		return nil, errNoLogs
	}
	reader, err := s.docker.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "all",
		Timestamps: true,
	})
	if err != nil {
		return nil, errNoLogs
	}
	return &logSource{docker: reader}, nil
}

// Check readiness first: for a pod with no node yet the log API answers 200 with an empty body,
// which reads as a finished run.
func (s *Server) resolvePodLogs(ctx context.Context, id string) (*logSource, error) {
	ns, err := s.kube.FindPodNamespace(ctx, id)
	if err != nil || ns == "" {
		return nil, errNoLogs
	}
	state, err := s.kube.PodLogReadiness(ctx, id, ns)
	switch {
	case err != nil, state == kubeapi.LogsGone:
		return nil, errNoLogs
	case state == kubeapi.LogsPending:
		return nil, errLogsPending
	}
	reader, err := s.kube.PodLogs(ctx, id, ns, true)
	if err != nil {
		return nil, errNoLogs
	}
	return &logSource{pod: &podTail{
		reader: reader,
		state: func(ctx context.Context) (kubeapi.LogReadiness, error) {
			return s.kube.PodLogReadiness(ctx, id, ns)
		},
		reopen: func(ctx context.Context, since time.Time) (io.ReadCloser, error) {
			return s.kube.PodLogsSince(ctx, id, ns, since)
		},
	}}, nil
}

var (
	logsWaitTimeout = 30 * time.Second
	logsWaitPoll    = 2 * time.Second
)

func (s *Server) waitForSessionLogs(ctx context.Context, id string, onWait func()) (*logSource, error) {
	deadline := time.Now().Add(logsWaitTimeout)
	waited := false
	for {
		src, err := s.resolveSessionLogs(ctx, id)
		if err == nil {
			return src, nil
		}
		if errors.Is(err, errLogsPending) {
			deadline = time.Now().Add(logsWaitTimeout)
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		if !waited {
			waited = true
			if onWait != nil {
				onWait()
			}
		}
		select {
		case <-ctx.Done():
			return nil, errNoLogs
		case <-time.After(logsWaitPoll):
		}
	}
}

func (src *logSource) Pump(ctx context.Context, out lineSink) {
	switch {
	case src.path != "":
		tailFileLines(ctx, src.path, out)
	case src.pod != nil:
		followPod(ctx, src.pod, out)
	case src.docker != nil:
		defer src.docker.Close()
		tailDockerLines(ctx, src.docker, out)
	}
}

func tailFileLines(ctx context.Context, path string, out lineSink) {
	//nolint:gosec // G304: path is server-written (usage DB / logs dir scan); a request only picks a session id
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)
	if !drainLines(reader, out) {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastSize, seekErr := f.Seek(0, io.SeekCurrent)
	logx.Warn(seekErr, "api.logs.log.seek_failed", "path", path)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !tailFileOnce(f, reader, &lastSize, path, out) {
				return
			}
		}
	}
}

func drainLines(reader *bufio.Reader, out lineSink) bool {
	for {
		line, err := reader.ReadString('\n')
		if line != "" && !out(strings.TrimRight(line, "\n")) {
			return false
		}
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
	}
}

func tailFileOnce(f *os.File, reader *bufio.Reader, lastSize *int64, path string, out lineSink) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	size := st.Size()
	if size < *lastSize {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return false
		}
		reader.Reset(f)
		*lastSize = 0
	}
	if size == *lastSize {
		return true
	}
	if !drainLines(reader, out) {
		return false
	}
	pos, serr := f.Seek(0, io.SeekCurrent)
	logx.Warn(serr, "api.logs.log.seek_failed", "path", path)
	if serr == nil {
		*lastSize = pos
	}
	return true
}

var (
	podReopenMin = time.Second
	podReopenMax = 10 * time.Second
)

func followPod(ctx context.Context, t *podTail, out lineSink) {
	dedup := &lineDedup{out: out}
	wait := podReopenMin
	reader := t.reader
	for {
		ended, rest := tailPlainLines(ctx, reader, dedup.emit)
		reader.Close()
		if !ended || ctx.Err() != nil {
			return
		}
		state, err := t.state(ctx)
		if err != nil || state != kubeapi.LogsRunning {
			if rest != "" {
				dedup.emit(rest)
			}
			return
		}
		if reader = t.reopenAfter(ctx, wait, dedup.last); reader == nil {
			return
		}
		wait = min(wait*2, podReopenMax)
	}
}

func (t *podTail) reopenAfter(ctx context.Context, wait time.Duration, since time.Time) io.ReadCloser {
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(wait):
	}
	if since.IsZero() {
		since = time.Unix(0, 0)
	}
	reader, err := t.reopen(ctx, since)
	if err != nil {
		logx.Warn(err, "api.logs.log.reopen_failed")
		return nil
	}
	return reader
}

// A reopen's since is truncated to the second, so lines up to that instant repeat; drop any not
// newer than the last sent.
type lineDedup struct {
	out  lineSink
	last time.Time
}

func (d *lineDedup) emit(line string) bool {
	if at, ok := logLineTime(line); ok {
		if !d.last.IsZero() && !at.After(d.last) {
			return true
		}
		d.last = at
	}
	return d.out(line)
}

func logLineTime(line string) (time.Time, bool) {
	stamp, _, ok := strings.Cut(line, " ")
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	return at, err == nil
}

// An unterminated last line is returned, not emitted: a stream cut mid-line repeats that line whole
// on reopen.
func tailPlainLines(ctx context.Context, reader io.Reader, out lineSink) (ended bool, rest string) {
	br := bufio.NewReaderSize(reader, 64*1024)
	var buf strings.Builder
	chunk := make([]byte, 8*1024)
	for {
		if ctx.Err() != nil {
			return false, ""
		}
		n, err := br.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if !flushLines(&buf, out) {
				return false, ""
			}
		}
		if err != nil {
			return true, buf.String()
		}
	}
}

func tailDockerLines(ctx context.Context, reader io.Reader, out lineSink) {
	fw := &lineFrameWriter{ctx: ctx, out: out}
	err := dockerx.Demux(fw, reader)
	if fw.aborted || (err != nil && ctx.Err() != nil) {
		return
	}
	if fw.buf.Len() > 0 {
		out(fw.buf.String())
	}
}

type lineFrameWriter struct {
	ctx     context.Context
	out     lineSink
	buf     strings.Builder
	aborted bool
}

func (l *lineFrameWriter) Write(p []byte) (int, error) {
	if l.ctx.Err() != nil {
		l.aborted = true
		return 0, l.ctx.Err()
	}
	l.buf.Write(p)
	if !flushLines(&l.buf, l.out) {
		l.aborted = true
		return 0, apperr.New("api.logs.errors.consumer_disconnected")
	}
	return len(p), nil
}

func flushLines(buf *strings.Builder, out lineSink) bool {
	text := buf.String()
	buf.Reset()
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines)-1; i++ {
		if !out(lines[i]) {
			return false
		}
	}
	if tail := lines[len(lines)-1]; tail != "" {
		buf.WriteString(tail)
	}
	return true
}
