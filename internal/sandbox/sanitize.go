package sandbox

import "strings"

// sanitizeDNS is the form a Kubernetes sandbox pod's name and labels take of an owner id. Capped at
// 55 so the "sandbox-" prefix keeps the name within 63 chars.
func sanitizeDNS(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 55 {
		out = out[:55]
	}
	return out
}
