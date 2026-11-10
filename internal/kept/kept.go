// Imports only the standard library: both the daemon and the CLI import it.
package kept

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	EnvKeep         = "OCTOPUS_KEEP_VERSIONS"
	DefaultKeep     = 3
	EnvVersions     = "OCTOPUS_KEPT_VERSIONS"
	EnvSnapshots    = "OCTOPUS_KEPT_SNAPSHOTS"
	RestoreExternal = "external"
)

type Version struct {
	Version  string `json:"version"`
	Snapshot string `json:"snapshot,omitempty"`
}

var (
	versionRe  = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	snapshotRe = regexp.MustCompile(`^octopus_[0-9_]+\.(sql|tar\.gz)$`)
)

// Snapshot names end up in a command operators copy and run; accept nothing but what the pre-update
// backup writes.
func ValidSnapshot(name string) bool { return snapshotRe.MatchString(name) }

// Minimum 2: the running version plus the previous one is what makes going back possible.
func Keep(getenv func(string) string) int {
	n, err := strconv.Atoi(strings.TrimSpace(getenv(EnvKeep)))
	if err != nil {
		return DefaultKeep
	}
	return max(n, 2)
}

func FromEnv(getenv func(string) string) []Version {
	snaps := map[string]string{}
	for _, pair := range split(getenv(EnvSnapshots)) {
		v, s, ok := strings.Cut(pair, "=")
		if ok && versionRe.MatchString(v) && ValidSnapshot(s) {
			snaps[v] = s
		}
	}
	var out []Version
	seen := map[string]bool{}
	for _, v := range split(getenv(EnvVersions)) {
		if !versionRe.MatchString(v) || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, Version{Version: v, Snapshot: snaps[v]})
	}
	return out
}

func Encode(vs []Version) (versions, snapshots string) {
	var vl, sl []string
	for _, v := range vs {
		if !versionRe.MatchString(v.Version) {
			continue
		}
		vl = append(vl, v.Version)
		if ValidSnapshot(v.Snapshot) {
			sl = append(sl, v.Version+"="+v.Snapshot)
		}
	}
	return strings.Join(vl, ","), strings.Join(sl, ",")
}

func Command(v Version, current string, noRestore bool) string {
	if v.Version == current {
		return ""
	}
	if noRestore {
		//i18n:ignore shell command an operator runs, not a message
		return "thulu update -to " + v.Version
	}
	snap := v.Snapshot
	if snap == "" {
		snap = "<snapshot>"
	}
	//i18n:ignore shell command an operator runs, not a message
	return "thulu update -to " + v.Version + " -restore " + snap
}

func split(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}
