package hostrun

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

func steerRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := createSteerQueue(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func queueOf(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(steerDir(root), steerQueueFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Case 35: the append follows the driver's protocol.
func TestAppendSteer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
		queue string
	}{
		{"appends", func(*testing.T, string) {}, runner.SteerAppended, "a\n"},
		{"sealed by the driver", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(steerDir(root), "closed"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, runner.SteerSealed, ""},
		{"waits for the driver's lock", func(t *testing.T, root string) {
			lock := filepath.Join(steerDir(root), "lock")
			if err := os.Mkdir(lock, 0o700); err != nil {
				t.Fatal(err)
			}
			time.AfterFunc(200*time.Millisecond, func() { _ = os.Remove(lock) })
		}, runner.SteerAppended, "a\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := steerRoot(t)
			c.setup(t, root)
			if got := appendSteer(root, []byte("a\n")); got != c.want {
				t.Errorf("appendSteer = %q, want %q", got, c.want)
			}
			if q := queueOf(t, root); q != c.queue {
				t.Errorf("queue = %q, want %q", q, c.queue)
			}
			if _, err := os.Stat(filepath.Join(steerDir(root), "lock")); !os.IsNotExist(err) {
				t.Errorf("the lock was left behind: %v", err)
			}
		})
	}
}

// A lock nobody releases gives up after the same 5 s as the Docker launcher.
func TestAppendSteerGivesUpOnAStuckLock(t *testing.T) {
	t.Parallel()
	root := steerRoot(t)
	if err := os.Mkdir(filepath.Join(steerDir(root), "lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := appendSteer(root, []byte("a\n")); got != runner.SteerLocked {
		t.Errorf("appendSteer = %q, want locked", got)
	}
	if q := queueOf(t, root); q != "" {
		t.Errorf("queue = %q, want nothing written", q)
	}
}

// A job whose folder is gone has ended: the message is refused and nothing is recreated.
func TestAppendSteerNeverCreatesTheQueue(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "gone")
	if got := appendSteer(root, []byte("a\n")); got != runner.SteerSealed {
		t.Errorf("appendSteer = %q, want sealed", got)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the job folder was recreated: %v", err)
	}
}

func TestAppendOnceSkipsAMessageAlreadyWritten(t *testing.T) {
	t.Parallel()
	j := &job{root: steerRoot(t)}
	written := map[string]bool{}
	line := `{"id":"m1","text":"hi"}` + "\n"
	for range 2 {
		if got := j.appendOnce(written, line); got != runner.SteerAppended {
			t.Fatalf("appendOnce = %q, want appended", got)
		}
	}
	noID := `{"text":"again"}` + "\n"
	j.appendOnce(written, noID)
	j.appendOnce(written, noID)
	if q := queueOf(t, j.root); q != line+noID+noID {
		t.Errorf("queue = %q, want m1 once and each id-less line as sent", q)
	}
}

func TestCreateSteerQueueKeepsWhatIsQueued(t *testing.T) {
	t.Parallel()
	root := steerRoot(t)
	if got := appendSteer(root, []byte("early\n")); got != runner.SteerAppended {
		t.Fatal(got)
	}
	if err := createSteerQueue(root); err != nil {
		t.Fatal(err)
	}
	if q := queueOf(t, root); q != "early\n" {
		t.Errorf("queue = %q, want the early message kept", q)
	}
}
