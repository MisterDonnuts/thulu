package hostrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/gatesprofile"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

func gatesJob(t *testing.T, projects ...runner.JobProject) *job {
	t.Helper()
	work := t.TempDir()
	return &job{r: &Runner{cfg: &Config{DaemonURL: "http://daemon", WorkDir: work}},
		spec: &runner.JobSpec{Projects: projects}, root: filepath.Join(work, "runs", "job1")}
}

func envMap(j *job) map[string]string {
	out := map[string]string{}
	for _, kv := range j.env() {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// thulu-gates' cache outlives the job: every run of a project on this machine reuses base results,
// stored reports and the quarantine, and none of it lands in the job's tmp or a client repo.
func TestJobEnvPointsThuluGatesAtTheRunnersCache(t *testing.T) {
	t.Setenv(gatesprofile.CacheEnv, "")
	j := gatesJob(t)
	got := envMap(j)[gatesprofile.CacheEnv]
	want := filepath.Join(j.r.cfg.WorkDir, "cache", "thulu-gates")
	if got != want {
		t.Errorf("%s = %q, want %q", gatesprofile.CacheEnv, got, want)
	}
	if strings.HasPrefix(got, j.root) {
		t.Errorf("the cache %q is inside the job's folder, which goes with the job", got)
	}
	// The person running the runner may keep it elsewhere.
	t.Setenv(gatesprofile.CacheEnv, "/srv/gates")
	if got := envMap(j)[gatesprofile.CacheEnv]; got != "/srv/gates" {
		t.Errorf("%s = %q, want the runner's own", gatesprofile.CacheEnv, got)
	}
}

func TestJobWritesTheProjectsGateProfile(t *testing.T) {
	t.Setenv(gatesprofile.Env, "/home/me/my-profile.json")
	j := gatesJob(t,
		runner.JobProject{ID: "p1", Name: "app", GatesProfile: json.RawMessage(`{"accept_unverified":{"tests":"Karma hangs on base"}}`)},
		runner.JobProject{ID: "p2", Name: "api"},
	)
	if err := j.writeGatesProfile(); err != nil {
		t.Fatal(err)
	}
	path := envMap(j)[gatesprofile.Env]
	if path != filepath.Join(j.root, "thulu-gates", "profile.json") {
		t.Fatalf("%s = %q", gatesprofile.Env, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("profile %s: %v", b, err)
	}
	if len(doc.Projects) != 1 || string(doc.Projects["app"]) != `{"accept_unverified":{"tests":"Karma hangs on base"}}` {
		t.Errorf("profile = %s", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("profile mode = %v, want 0600", fi.Mode().Perm())
	}

	// No project with a profile: no file, and the shell's variable does not reach the job either.
	none := gatesJob(t, runner.JobProject{ID: "p2", Name: "api"})
	if err := none.writeGatesProfile(); err != nil {
		t.Fatal(err)
	}
	if v, ok := envMap(none)[gatesprofile.Env]; ok {
		t.Errorf("%s = %q with no profile, want unset", gatesprofile.Env, v)
	}
}

// The job runs the gates of the runner binary that started it: the CLI is written into the job,
// first on its PATH and named in $THULU_GATES_HOME, so a copy installed by hand hands over to it.
func TestJobShipsItsOwnThuluGates(t *testing.T) {
	t.Setenv("PATH", "/home/me/.local/bin:/usr/bin")
	j := gatesJob(t)
	if err := j.writeGatesCLI(); err != nil {
		t.Fatal(err)
	}
	// Twice: a resumed job writes it again over the first copy.
	if err := j.writeGatesCLI(); err != nil {
		t.Fatal(err)
	}
	env := envMap(j)
	home := filepath.Join(j.root, "thulu-gates", "cli")
	if env[gatesprofile.HomeEnv] != home {
		t.Errorf("%s = %q, want %q", gatesprofile.HomeEnv, env[gatesprofile.HomeEnv], home)
	}
	bin := filepath.Join(j.root, "bin")
	if !strings.HasPrefix(env["PATH"], bin+string(os.PathListSeparator)) || !strings.HasSuffix(env["PATH"], "/home/me/.local/bin:/usr/bin") {
		t.Errorf("PATH = %q, want %s first and the machine's after", env["PATH"], bin)
	}
	for _, f := range []string{"package.json", "bin/thulu-gates.js", "lib/cli.js", "lib/handover.js", "locales/en.json"} {
		if _, err := os.Stat(filepath.Join(home, f)); err != nil {
			t.Errorf("the job's copy lacks %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "test")); err == nil {
		t.Error("the job's copy carries the CLI's tests")
	}
	fi, err := os.Stat(filepath.Join(bin, "thulu-gates"))
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("bin/thulu-gates = %v, %v; want an executable link to the job's CLI", fi, err)
	}
	if target, _ := os.Readlink(filepath.Join(bin, "thulu-gates")); target != filepath.Join(home, "bin", "thulu-gates.js") {
		t.Errorf("bin/thulu-gates points at %q", target)
	}
}
