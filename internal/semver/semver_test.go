package semver

import "testing"

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		in   string
		ok   bool
		want string
	}{
		{"1.2.3", true, "1.2.3"},
		{"v0.2.0", true, "0.2.0"},
		{"0.2.0-rc.1", true, "0.2.0-rc.1"},
		{"0.2.0-dev.abc1234", true, "0.2.0-dev.abc1234"},
		{"1.0.0+build.5", true, "1.0.0+build.5"},
		{"dev", false, ""},
		{"latest", false, ""},
		{"1.2", false, ""},
		{"01.2.3", false, ""},
		{"1.2.3-", false, ""},
		{"1.2.3-a..b", false, ""},
		{"", false, ""},
	} {
		v, ok := Parse(tc.in)
		if ok != tc.ok {
			t.Errorf("Parse(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && v.String() != tc.want {
			t.Errorf("Parse(%q) = %q, want %q", tc.in, v.String(), tc.want)
		}
	}
}

func TestCompareOrder(t *testing.T) {
	order := []string{
		"0.1.0",
		"0.2.0-alpha",
		"0.2.0-alpha.1",
		"0.2.0-alpha.beta",
		"0.2.0-beta.2",
		"0.2.0-beta.11",
		"0.2.0-dev.abc1234",
		"0.2.0-rc.1",
		"0.2.0",
		"0.2.1",
		"0.10.0",
		"1.0.0",
	}
	for i := 0; i+1 < len(order); i++ {
		a, b := MustParse(order[i]), MustParse(order[i+1])
		if Compare(a, b) != -1 || Compare(b, a) != 1 {
			t.Errorf("want %s < %s", order[i], order[i+1])
		}
	}
	if Compare(MustParse("1.0.0+a"), MustParse("1.0.0+b")) != 0 {
		t.Error("build metadata must not affect order")
	}
}

func TestLessNewerUnparseable(t *testing.T) {
	if !Less("0.1.0", "0.2.0") || Less("0.2.0", "0.1.0") {
		t.Error("Less wrong")
	}
	if !Newer("0.2.0", "0.2.0-rc1") {
		t.Error("a release is newer than its candidate")
	}
	if Less("dev", "0.1.0") || Newer("dev", "0.1.0") {
		t.Error("an unparseable version must never be ordered")
	}
}

func TestSatisfies(t *testing.T) {
	v := MustParse("0.3.1")
	for _, tc := range []struct {
		c    string
		want bool
	}{
		{"", true},
		{">=0.2.0", true},
		{">=0.3.1", true},
		{">0.3.1", false},
		{"<0.4.0", true},
		{">=0.2.0 <0.3.0", false},
		{">=0.2.0, <0.4.0", true},
		{"0.3.1", true},
		{"=0.3.0", false},
		{">=nope", false},
		{"~0.3.0", false},
	} {
		if got := Satisfies(v, tc.c); got != tc.want {
			t.Errorf("Satisfies(0.3.1, %q) = %v, want %v", tc.c, got, tc.want)
		}
	}
}

func TestMinorLine(t *testing.T) {
	if got := MustParse("0.3.7-rc.1").MinorLine(); got != "0.3" {
		t.Errorf("MinorLine = %q", got)
	}
}
