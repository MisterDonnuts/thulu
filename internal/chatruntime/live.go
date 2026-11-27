package chatruntime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// A turn's engine output, line by line, while it runs: what the chat shows live and what a late
// viewer replays. Each line also lands in the turn's transcript file, read back once the turn ended.

// Bounds what one turn keeps in memory for late viewers; past it they get the transcript's tail
// from the file once the turn ends.
const liveTurnMaxBytes = 8 << 20

// One line past this is not published live (it is still written to the transcript): a tool result
// that size is a file dump the browser has no use for mid-turn.
const liveLineMaxBytes = 256 << 10

type liveTurn struct {
	mu        sync.Mutex
	lines     []string
	bytes     int
	truncated bool
	done      bool
	wake      chan struct{}
	file      *os.File
}

func (t *liveTurn) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	if t.file != nil {
		if _, err := t.file.WriteString(line + "\n"); err != nil {
			logx.Warn(err, "chatruntime.log.transcript_write")
			_ = t.file.Close()
			t.file = nil
		}
	}
	if len(line) > liveLineMaxBytes {
		return
	}
	if t.bytes+len(line) > liveTurnMaxBytes {
		t.truncated = true
		return
	}
	t.lines = append(t.lines, line)
	t.bytes += len(line)
	close(t.wake)
	t.wake = make(chan struct{})
}

// reset drops what an attempt on a dead container printed; the retry prints the turn again.
func (t *liveTurn) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines, t.bytes, t.truncated = nil, 0, false
	if t.file != nil {
		logx.Warn(t.file.Truncate(0), "chatruntime.log.transcript_write")
		_, err := t.file.Seek(0, 0)
		logx.Warn(err, "chatruntime.log.transcript_write")
	}
}

func (t *liveTurn) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	if t.file != nil {
		logx.Warn(t.file.Close(), "chatruntime.log.transcript_write")
		t.file = nil
	}
	close(t.wake)
}

func (t *liveTurn) next(from int) (lines []string, done bool, wake <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if from < len(t.lines) {
		lines = t.lines[from:len(t.lines):len(t.lines)]
	}
	return lines, t.done, t.wake
}

type liveTurns struct {
	mu sync.Mutex
	m  map[string]*liveTurn
}

func turnKey(chatID, turnID string) string { return chatID + "/" + turnID }

// validID keeps ids that reach a file path to one plain path element.
func validID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id
}

// TranscriptPath is where a turn's engine output is kept; "" when the daemon has no data dir or
// an id cannot name a file.
func (m *Manager) TranscriptPath(chatID, turnID string) string {
	if m.dataDir == "" || !validID(chatID) || !validID(turnID) {
		return ""
	}
	return filepath.Join(m.dataDir, "chats", chatID, "turns", turnID+".jsonl")
}

// BeginLive opens a turn to viewers before its container exists: a viewer that subscribes while
// the container starts waits for the first line instead of seeing the turn end.
func (m *Manager) BeginLive(chatID, turnID string) {
	t := &liveTurn{wake: make(chan struct{})}
	if path := m.TranscriptPath(chatID, turnID); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err == nil {
			f, ferr := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			logx.Warn(ferr, "chatruntime.log.transcript_write")
			if ferr == nil {
				t.file = f
			}
		} else {
			logx.Warn(err, "chatruntime.log.transcript_write")
		}
	}
	m.live.mu.Lock()
	if m.live.m == nil {
		m.live.m = map[string]*liveTurn{}
	}
	if old := m.live.m[turnKey(chatID, turnID)]; old != nil {
		old.finish()
	}
	m.live.m[turnKey(chatID, turnID)] = t
	m.live.mu.Unlock()
}

func (m *Manager) liveTurn(chatID, turnID string) *liveTurn {
	m.live.mu.Lock()
	defer m.live.mu.Unlock()
	return m.live.m[turnKey(chatID, turnID)]
}

// EndLive closes the turn: viewers get the end, later ones read the transcript file.
func (m *Manager) EndLive(chatID, turnID string) {
	m.live.mu.Lock()
	t := m.live.m[turnKey(chatID, turnID)]
	delete(m.live.m, turnKey(chatID, turnID))
	m.live.mu.Unlock()
	if t != nil {
		t.finish()
	}
}

// FollowTurn hands emit every line of the turn, live while it runs and from its transcript once it
// ended, and returns when the turn is over or emit says stop.
func (m *Manager) FollowTurn(ctx context.Context, chatID, turnID string, emit func(line string) bool) {
	if t := m.liveTurn(chatID, turnID); t != nil {
		followLive(ctx, t, emit)
		return
	}
	lines, err := m.Transcript(chatID, turnID)
	if err != nil {
		return
	}
	for _, l := range lines {
		if ctx.Err() != nil || !emit(l) {
			return
		}
	}
}

// followLive emits a running turn's lines as they come, until it ends or emit says stop.
func followLive(ctx context.Context, t *liveTurn, emit func(line string) bool) {
	from := 0
	for {
		lines, done, wake := t.next(from)
		for _, l := range lines {
			if !emit(l) {
				return
			}
		}
		from += len(lines)
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}

// Transcript reads a finished turn's engine output. A turn with no file (older than transcripts,
// or a daemon without a data dir) is fs.ErrNotExist.
func (m *Manager) Transcript(chatID, turnID string) ([]string, error) {
	path := m.TranscriptPath(chatID, turnID)
	if path == "" {
		return nil, fs.ErrNotExist
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return out, err
	}
	return out, nil
}

// lineWriter splits a stream into lines for sink; Flush hands over a last line without newline.
type lineWriter struct {
	sink func(line string)
	buf  []byte
	n    int
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.feed(p)
	return len(p), nil
}

// feed is Write for callers that hold the whole output already; it cannot fail.
func (w *lineWriter) feed(p []byte) {
	w.n += len(p)
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimRight(w.buf[:i], "\r")
		if len(line) > 0 {
			w.sink(string(line))
		}
		w.buf = w.buf[i+1:]
	}
}

// turnFeed hands each line of a turn to its live feed and, through a pipe, to the engine's parser.
// The parser drains the pipe to its end, so a write fails only once close ran.
type turnFeed struct {
	live   *liveTurn
	pw     *io.PipeWriter
	parsed chan *TurnResult
	failed bool
}

func newTurnFeed(live *liveTurn, eng engine.Engine) *turnFeed {
	pr, pw := io.Pipe()
	f := &turnFeed{live: live, pw: pw, parsed: make(chan *TurnResult, 1)}
	go func() {
		res := collectTurn(eng.ParseStream(pr))
		// A parser that stops before the end must not leave the writer blocked.
		if _, err := io.Copy(io.Discard, pr); err != nil {
			logx.Warn(err, "chatruntime.log.turn_parse_drain")
		}
		f.parsed <- res
	}()
	return f
}

func (f *turnFeed) line(line string) {
	if f.live != nil {
		f.live.add(line)
	}
	if f.failed {
		return
	}
	if _, err := io.WriteString(f.pw, line+"\n"); err != nil {
		f.failed = true
		logx.Warn(err, "chatruntime.log.turn_parse_feed")
	}
}

// close ends the parser's input and waits for the turn it read.
func (f *turnFeed) close() *TurnResult {
	logx.Warn(f.pw.Close(), "chatruntime.log.turn_parse_feed")
	return <-f.parsed
}

func (w *lineWriter) Flush() {
	if line := bytes.TrimSpace(w.buf); len(line) > 0 {
		w.sink(string(line))
	}
	w.buf = nil
}
