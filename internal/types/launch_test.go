package types

import (
	"slices"
	"testing"
)

func TestWithEnvReplacesAnEarlierValue(t *testing.T) {
	got := withEnv([]string{"A=1", "OCTOPUS_ENGINE_DIR=/elsewhere", "B=2"}, "OCTOPUS_ENGINE_DIR", "/opt/thulu-engines")
	want := []string{"A=1", "B=2", "OCTOPUS_ENGINE_DIR=/opt/thulu-engines"}
	if !slices.Equal(got, want) {
		t.Errorf("withEnv = %v, want %v", got, want)
	}
}
