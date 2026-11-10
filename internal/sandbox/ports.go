package sandbox

import (
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var DefaultExposedPorts = []int{3000, 5173, 8000, 8080}

const EnvSandboxPorts = "OCTOPUS_SANDBOX_PORTS"

func AppPortsFromEnv(getenv func(string) string) []int {
	raw := strings.TrimSpace(getenv(EnvSandboxPorts))
	if raw == "" {
		return slices.Clone(DefaultExposedPorts)
	}
	var out []int
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' }) {
		p, err := strconv.Atoi(f)
		if err != nil || p <= 0 || p > 65535 {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.app_port_ignored", "env", EnvSandboxPorts, "port", f))
			continue
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return slices.Clone(DefaultExposedPorts)
	}
	return out
}

func appPorts(opts CreateOpts) []int {
	if len(opts.ExposePorts) > 0 {
		return opts.ExposePorts
	}
	return DefaultExposedPorts
}

func portList(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ", ")
}
