package infra

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type procFS struct {
	root string
}

// os.Root keeps reads inside OCTOPUS_INFRA_PROC_ROOT, symlinks included; /proc/net -> self/net
// stays inside and still resolves.
func (p procFS) read(name string) ([]byte, error) {
	r, err := os.OpenRoot(p.root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.ReadFile(name)
}

func (p procFS) available() bool {
	_, err := os.Stat(filepath.Join(p.root, "stat"))
	return err == nil
}

type cpuTimes struct {
	total uint64
	idle  uint64
}

func parseStat(b []byte) (cpuTimes, bool) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v [8]uint64
		for i := 0; i < len(v) && i+1 < len(f); i++ {
			n, err := strconv.ParseUint(f[i+1], 10, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			v[i] = n
		}
		// user nice system idle iowait irq softirq steal; guest columns are skipped because user
		// and nice already count them.
		var t cpuTimes
		for _, x := range v {
			t.total += x
		}
		t.idle = v[3] + v[4]
		return t, true
	}
	return cpuTimes{}, false
}

func cpuPercent(prev, cur cpuTimes) float64 {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return 0
	}
	dt := float64(cur.total - prev.total)
	di := float64(cur.idle - prev.idle)
	if di > dt {
		return 0
	}
	return clampPct((dt - di) / dt * 100)
}

// Kernels before 3.14 have no MemAvailable.
func parseMeminfo(b []byte) (Memory, bool) {
	kv := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		kv[name] = n
	}
	total, ok := kv["MemTotal"]
	if !ok || total == 0 {
		return Memory{}, false
	}
	avail, ok := kv["MemAvailable"]
	if !ok {
		avail = kv["MemFree"] + kv["Buffers"] + kv["Cached"] //i18n:ignore /proc field name, not a message
	}
	if avail > total {
		avail = total
	}
	m := Memory{
		Total:     total,
		Available: avail,
		Used:      total - avail,
		Cached:    kv["Cached"] + kv["SReclaimable"], //i18n:ignore /proc field name, not a message
		Buffers:   kv["Buffers"],                     //i18n:ignore /proc field name, not a message
		SwapTotal: kv["SwapTotal"],
	}
	if free := kv["SwapFree"]; free <= m.SwapTotal {
		m.SwapUsed = m.SwapTotal - free
	}
	m.Percent = clampPct(float64(m.Used) / float64(total) * 100)
	return m, true
}

func parseLoadavg(b []byte) ([]float64, bool) {
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return nil, false
	}
	out := make([]float64, 3)
	for i := range out {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

func parseUptime(b []byte) (float64, bool) {
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

type cpuInfo struct {
	model   string
	cores   int
	threads int
}

// Some VMs and ARM kernels list no physical id/core id: fall back to "cpu cores" per package, then
// to threads.
func parseCPUInfo(b []byte) cpuInfo {
	var (
		ci       cpuInfo
		phys     string
		perPkg   int
		pairs    = map[string]bool{}
		packages = map[string]bool{}
	)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			ci.threads++
			phys = ""
		case "model name", "Model": //i18n:ignore /proc field name, not a message
			if ci.model == "" {
				ci.model = v
			}
		case "physical id": //i18n:ignore /proc field name, not a message
			phys = v
			packages[v] = true
		case "core id": //i18n:ignore /proc field name, not a message
			pairs[phys+"/"+v] = true
		case "cpu cores": //i18n:ignore /proc field name, not a message
			if n, err := strconv.Atoi(v); err == nil {
				perPkg = n
			}
		}
	}
	switch {
	case len(pairs) > 0:
		ci.cores = len(pairs)
	case perPkg > 0:
		ci.cores = perPkg * max(1, len(packages))
	default:
		ci.cores = ci.threads
	}
	return ci
}

type netCounters struct {
	rx, tx uint64
}

func parseNetDev(b []byte) map[string]netCounters {
	out := map[string]netCounters{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[strings.TrimSpace(name)] = netCounters{rx: rx, tx: tx}
	}
	return out
}

// These interfaces relay bytes the uplink already counted; summing them would double every byte a
// container sends.
var virtualIfacePrefixes = []string{
	"lo", "veth", "docker", "br-", "cni", "flannel", "cali", "vxlan", "kube-",
	"tunl", "virbr", "tap", "kata", "cilium", "weave", "dummy",
}

func physicalIface(name string) bool {
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

func parseOSRelease(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func clampPct(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	}
	return v
}
