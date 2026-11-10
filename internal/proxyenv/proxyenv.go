// Both spellings are forwarded: curl and most Go/Python tooling read lowercase, Node and many CLIs
// read uppercase.
package proxyenv

import "strings"

var Names = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
}

// Values may embed proxy credentials; treat them as secrets.
func FromEnv(getenv func(string) string) map[string]string {
	out := map[string]string{}
	for _, upper := range Names[:3] {
		lower := strings.ToLower(upper)
		u, l := strings.TrimSpace(getenv(upper)), strings.TrimSpace(getenv(lower))
		switch {
		case u == "" && l == "":
			continue
		case u == "":
			u = l
		case l == "":
			l = u
		}
		out[upper], out[lower] = u, l
	}
	return out
}

func List(getenv func(string) string) []string {
	m := FromEnv(getenv)
	out := make([]string, 0, len(m))
	for _, k := range Names {
		if v, ok := m[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
