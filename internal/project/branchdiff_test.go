package project

import (
	"slices"
	"testing"
)

func TestParseNumstat(t *testing.T) {
	raw := "12\t3\tsrc/app.ts\x00-\t-\tassets/logo.png\x001\t0\tdir with space/a b.ts\x00\x00garbage\x00"
	want := []DiffFile{
		{Path: "src/app.ts", Added: 12, Deleted: 3},
		{Path: "assets/logo.png"},
		{Path: "dir with space/a b.ts", Added: 1},
	}
	if got := parseNumstat(raw); !slices.Equal(got, want) {
		t.Errorf("parseNumstat = %+v, want %+v", got, want)
	}
	if got := parseNumstat(""); len(got) != 0 {
		t.Errorf("empty numstat = %+v", got)
	}
}
