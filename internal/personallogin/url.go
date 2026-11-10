package personallogin

import (
	"net/url"
	"regexp"
)

var (
	// Claude Code wraps the URL in an OSC 8 hyperlink, whose target would otherwise run into the
	// visible text.
	oscSeq   = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
	csiSeq   = regexp.MustCompile(`\x1b\[[0-9;?]*[\x20-\x2f]*[\x40-\x7e]`)
	httpsURL = regexp.MustCompile(`https://[^\s"'<>]+`)
)

// Only an https URL with an OAuth authorize path and a state counts, so a banner's link cannot pass
// for it.
func AuthorizationURL(term []byte) string {
	text := csiSeq.ReplaceAll(oscSeq.ReplaceAll(term, nil), nil)
	for _, m := range httpsURL.FindAll(text, -1) {
		u, err := url.Parse(string(m))
		if err != nil || u.Scheme != "https" || u.Host == "" {
			continue
		}
		if authorizePath.MatchString(u.Path) && u.Query().Get("state") != "" {
			return u.String()
		}
	}
	return ""
}

var authorizePath = regexp.MustCompile(`/oauth/authorize$`)
