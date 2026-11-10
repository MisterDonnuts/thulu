package dockerx

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/system"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLogConfigFor(t *testing.T) {
	cases := []struct {
		name     string
		driver   string
		env      map[string]string
		wantSize string
		wantFile string
	}{
		{name: "json-file defaults", driver: "json-file", wantSize: "10m", wantFile: "3"},
		{name: "local defaults", driver: "local", wantSize: "10m", wantFile: "3"},
		{name: "overrides", driver: "json-file",
			env:      map[string]string{EnvLogMaxSize: "50m", EnvLogMaxFile: "5"},
			wantSize: "50m", wantFile: "5"},
		{name: "unlimited size", driver: "json-file",
			env: map[string]string{EnvLogMaxSize: "-1"}, wantSize: "-1", wantFile: "3"},
		{name: "invalid overrides fall back", driver: "json-file",
			env:      map[string]string{EnvLogMaxSize: "lots", EnvLogMaxFile: "0"},
			wantSize: "10m", wantFile: "3"},
		{name: "journald", driver: "journald"},
		{name: "fluentd", driver: "fluentd"},
		{name: "unknown driver", driver: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LogConfigFor(tc.driver, envOf(tc.env))
			if got.Type != "" {
				t.Errorf("Type = %q, want empty (the daemon's default driver)", got.Type)
			}
			if tc.wantSize == "" {
				if len(got.Config) != 0 {
					t.Errorf("Config = %v, want none", got.Config)
				}
				return
			}
			if got.Config["max-size"] != tc.wantSize || got.Config["max-file"] != tc.wantFile {
				t.Errorf("Config = %v, want max-size=%s max-file=%s", got.Config, tc.wantSize, tc.wantFile)
			}
		})
	}
}

type fakeInfo struct {
	driver string
	err    error
	calls  int
}

func (f *fakeInfo) Info(context.Context) (system.Info, error) {
	f.calls++
	return system.Info{LoggingDriver: f.driver}, f.err
}

func TestContainerLogConfig_CachesDriver(t *testing.T) {
	ok := &fakeInfo{driver: "json-file"}
	for range 3 {
		if got := ContainerLogConfig(t.Context(), ok); got.Config["max-size"] == "" {
			t.Fatalf("no rotation options for json-file: %+v", got)
		}
	}
	if ok.calls != 1 {
		t.Errorf("Info called %d times, want 1", ok.calls)
	}

	failing := &fakeInfo{err: errors.New("daemon down")}
	for range 2 {
		if got := ContainerLogConfig(t.Context(), failing); len(got.Config) != 0 {
			t.Errorf("options despite failed lookup: %+v", got)
		}
	}
	if failing.calls != 2 {
		t.Errorf("failed lookup cached: Info called %d times, want 2", failing.calls)
	}
}
