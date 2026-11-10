package kept

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRoundTrip(t *testing.T) {
	in := []Version{
		{Version: "0.3.0"},
		{Version: "0.2.0", Snapshot: "octopus_20261001_120000.sql"},
		{Version: "0.1.0", Snapshot: "octopus_20260901_080000.tar.gz"},
	}
	vs, ss := Encode(in)
	if vs != "0.3.0,0.2.0,0.1.0" || ss != "0.2.0=octopus_20261001_120000.sql,0.1.0=octopus_20260901_080000.tar.gz" {
		t.Fatalf("encode = %q / %q", vs, ss)
	}
	out := FromEnv(env(map[string]string{EnvVersions: vs, EnvSnapshots: ss}))
	if len(out) != 3 || out[1] != in[1] || out[2] != in[2] || out[0] != in[0] {
		t.Errorf("decode = %+v", out)
	}
}

func TestFromEnvDropsWhatItCannotTrust(t *testing.T) {
	out := FromEnv(env(map[string]string{
		EnvVersions:  "0.3.0, latest,0.3.0,../x,0.2.0",
		EnvSnapshots: "0.2.0=../../etc/passwd,0.3.0=octopus_1.sql;rm -rf /",
	}))
	if len(out) != 2 || out[0].Version != "0.3.0" || out[1].Version != "0.2.0" || out[0].Snapshot != "" || out[1].Snapshot != "" {
		t.Errorf("got %+v", out)
	}
}

func TestKeep(t *testing.T) {
	for v, want := range map[string]int{"": 3, "5": 5, "1": 2, "x": 3, "0": 2} {
		if got := Keep(env(map[string]string{EnvKeep: v})); got != want {
			t.Errorf("Keep(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestCommand(t *testing.T) {
	v := Version{Version: "0.2.0", Snapshot: "octopus_20261001_120000.sql"}
	if got := Command(v, "0.3.0", true); got != "thulu update -to 0.2.0" {
		t.Error(got)
	}
	if got := Command(v, "0.3.0", false); got != "thulu update -to 0.2.0 -restore octopus_20261001_120000.sql" {
		t.Error(got)
	}
	if got := Command(Version{Version: "0.1.0"}, "0.3.0", false); got != "thulu update -to 0.1.0 -restore <snapshot>" {
		t.Error(got)
	}
	if Command(Version{Version: "0.3.0"}, "0.3.0", false) != "" {
		t.Error("command for the running version")
	}
}
