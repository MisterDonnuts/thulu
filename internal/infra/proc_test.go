package infra

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "proc", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestParseStat(t *testing.T) {
	got, ok := parseStat(readFixture(t, "stat"))
	if !ok {
		t.Fatal("no cpu line found")
	}
	if got.total != 10000 || got.idle != 8500 {
		t.Errorf("parseStat = %+v, want total 10000 idle 8500", got)
	}
	if _, ok := parseStat([]byte("intr 1\nctxt 2\n")); ok {
		t.Error("a stat without a cpu line parsed")
	}
}

func TestCPUPercentDeltaMath(t *testing.T) {
	prev := cpuTimes{total: 10000, idle: 8500}
	cases := []struct {
		name string
		cur  cpuTimes
		want float64
	}{
		{"quarter busy", cpuTimes{total: 10400, idle: 8800}, 25},
		{"fully busy", cpuTimes{total: 10400, idle: 8500}, 100},
		{"idle", cpuTimes{total: 10400, idle: 8900}, 0},
		{"no time passed", prev, 0},
		{"counter went backwards", cpuTimes{total: 9000, idle: 8000}, 0},
		{"idle grew more than total", cpuTimes{total: 10100, idle: 9000}, 0},
	}
	for _, c := range cases {
		if got := cpuPercent(prev, c.cur); !near(got, c.want) {
			t.Errorf("%s: cpuPercent = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseMeminfo(t *testing.T) {
	m, ok := parseMeminfo(readFixture(t, "meminfo"))
	if !ok {
		t.Fatal("meminfo did not parse")
	}
	const kb = 1024
	if m.Total != 32768000*kb || m.Available != 24576000*kb || m.Used != (32768000-24576000)*kb {
		t.Errorf("total/available/used = %d/%d/%d", m.Total, m.Available, m.Used)
	}
	if !near(m.Percent, 25) {
		t.Errorf("percent = %v, want 25", m.Percent)
	}
	if m.Cached != (12000000+1000000)*kb || m.Buffers != 512000*kb {
		t.Errorf("cached/buffers = %d/%d", m.Cached, m.Buffers)
	}
	if m.SwapTotal != 8388608*kb || m.SwapUsed != (8388608-8000000)*kb {
		t.Errorf("swap = %d/%d", m.SwapUsed, m.SwapTotal)
	}

	old, ok := parseMeminfo([]byte("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 250 kB\n"))
	if !ok || old.Available != 400*kb || old.Used != 600*kb {
		t.Errorf("pre-3.14 meminfo = %+v", old)
	}
	if _, ok := parseMeminfo([]byte("garbage\n")); ok {
		t.Error("meminfo without MemTotal parsed")
	}
}

func TestParseLoadavgAndUptime(t *testing.T) {
	l, ok := parseLoadavg(readFixture(t, "loadavg"))
	if !ok || len(l) != 3 || l[0] != 1.5 || l[1] != 0.75 || l[2] != 0.25 {
		t.Errorf("loadavg = %v %v", l, ok)
	}
	if _, ok := parseLoadavg([]byte("1.0")); ok {
		t.Error("short loadavg parsed")
	}
	u, ok := parseUptime(readFixture(t, "uptime"))
	if !ok || u != 93784.12 {
		t.Errorf("uptime = %v %v", u, ok)
	}
}

func TestParseCPUInfo(t *testing.T) {
	ci := parseCPUInfo(readFixture(t, "cpuinfo"))
	if ci.model != "Example CPU 4C/8T @ 3.00GHz" || ci.cores != 4 || ci.threads != 8 {
		t.Errorf("cpuinfo = %+v, want 4 cores / 8 threads", ci)
	}
	vm := parseCPUInfo([]byte("processor: 0\nmodel name: X\ncpu cores: 2\n\nprocessor: 1\nmodel name: X\ncpu cores: 2\n"))
	if vm.cores != 2 || vm.threads != 2 {
		t.Errorf("vm cpuinfo = %+v", vm)
	}
	arm := parseCPUInfo([]byte("processor: 0\n\nprocessor: 1\n\nprocessor: 2\n"))
	if arm.cores != 3 || arm.threads != 3 || arm.model != "" {
		t.Errorf("bare cpuinfo = %+v", arm)
	}
}

func TestParseNetDevAndRates(t *testing.T) {
	cur := parseNetDev(readFixture(t, "net/dev"))
	if len(cur) != 4 || cur["eth0"] != (netCounters{rx: 1000000, tx: 500000}) {
		t.Fatalf("net/dev = %+v", cur)
	}
	prev := map[string]netCounters{"eth0": {rx: 900000, tx: 480000}, "lo": {rx: 0, tx: 0}}
	n := netRates(prev, cur, 10*time.Second, true)
	if len(n.Interfaces) != 1 || n.Interfaces[0].Name != "eth0" {
		t.Fatalf("interfaces = %+v, want eth0 only", n.Interfaces)
	}
	if !near(n.RxBps, 10000) || !near(n.TxBps, 2000) {
		t.Errorf("rates = %v rx / %v tx, want 10000 / 2000", n.RxBps, n.TxBps)
	}
	if first := netRates(nil, cur, 0, false); first.RxBps != 0 || first.Interfaces[0].RxBytes != 1000000 {
		t.Errorf("first read = %+v, want totals and no rate", first)
	}
	reset := netRates(map[string]netCounters{"eth0": {rx: 5000000, tx: 0}}, cur, time.Second, true)
	if reset.RxBps != 0 {
		t.Errorf("reset counter rate = %v, want 0", reset.RxBps)
	}
}

func TestParseOSRelease(t *testing.T) {
	got := parseOSRelease([]byte("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nID=ubuntu\n"))
	if got != "Ubuntu 24.04.1 LTS" {
		t.Errorf("PRETTY_NAME = %q", got)
	}
}

func TestProcFSStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "self", "net"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "self", "net", "dev"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("self/net", filepath.Join(root, "net")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "stat")); err != nil {
		t.Fatal(err)
	}
	p := procFS{root: root}
	if b, err := p.read("net/dev"); err != nil || string(b) != "ok" {
		t.Errorf("read through an inside link = %q, %v", b, err)
	}
	if _, err := p.read("stat"); err == nil {
		t.Error("a link out of the root was followed")
	}
	if _, err := p.read("../secret"); err == nil {
		t.Error("a path out of the root was read")
	}
}
