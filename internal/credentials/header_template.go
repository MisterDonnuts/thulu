package credentials

import (
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type HeaderLine struct {
	Name  string
	Value string
}

var placeholderRe = regexp.MustCompile(`\{([a-z0-9_:]+)\}`)

var headersTheProxyOwns = map[string]bool{
	"Host": true, "Content-Length": true, "Content-Type": true, "Accept": true, //i18n:ignore HTTP header names
	"Connection": true, "Transfer-Encoding": true, "Mcp-Session-Id": true, //i18n:ignore HTTP header names
	"Mcp-Protocol-Version": true, "Last-Event-Id": true,
}

func ParseHeaderTemplate(tmpl string) ([]HeaderLine, error) {
	var out []HeaderLine
	for _, raw := range strings.Split(strings.ReplaceAll(tmpl, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !ValidHeaderName(name) || value == "" {
			return nil, apperr.Invalid("credentials.errors.header_template_line", "line", line)
		}
		if headersTheProxyOwns[http.CanonicalHeaderKey(name)] {
			return nil, apperr.Invalid("credentials.errors.header_template_reserved", "header", name)
		}
		for _, m := range placeholderRe.FindAllStringSubmatch(value, -1) {
			if !validPlaceholder(m[1]) {
				return nil, apperr.Invalid("credentials.errors.header_template_placeholder", "placeholder", m[0],
					"field_syntax", "{field}", "base64_syntax", "{base64:a:b}") //i18n:ignore placeholder syntax
			}
		}
		out = append(out, HeaderLine{Name: name, Value: value})
	}
	if len(out) == 0 {
		return nil, apperr.Invalid("credentials.errors.header_template_empty")
	}
	return out, nil
}

func validPlaceholder(p string) bool {
	parts := strings.Split(p, ":")
	switch {
	case len(parts) == 1:
		return parts[0] != ""
	case len(parts) == 3 && parts[0] == "base64":
		return parts[1] != "" && parts[2] != ""
	default:
		return false
	}
}

func RenderHeaderTemplate(tmpl string, payload map[string]string) (http.Header, error) {
	lines, err := ParseHeaderTemplate(tmpl)
	if err != nil {
		return nil, err
	}
	field := func(name string) (string, error) {
		v := strings.TrimSpace(payload[name])
		if v == "" {
			return "", apperr.Invalid("credentials.errors.header_template_missing_field", "field", name)
		}
		return v, nil
	}
	h := http.Header{}
	for _, l := range lines {
		var ferr error
		value := placeholderRe.ReplaceAllStringFunc(l.Value, func(m string) string {
			parts := strings.Split(m[1:len(m)-1], ":")
			if len(parts) == 3 {
				a, errA := field(parts[1])
				b, errB := field(parts[2])
				if errA != nil || errB != nil {
					ferr = firstErr(errA, errB)
					return ""
				}
				return base64.StdEncoding.EncodeToString([]byte(a + ":" + b))
			}
			v, err := field(parts[0])
			if err != nil {
				ferr = err
			}
			return v
		})
		if ferr != nil {
			return nil, ferr
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, apperr.Invalid("credentials.errors.header_value_newline")
		}
		h.Add(l.Name, value)
	}
	return h, nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func HeadersFor(provider, kind, template string, payload map[string]string) (http.Header, error) {
	if strings.TrimSpace(template) != "" {
		return RenderHeaderTemplate(template, payload)
	}
	if provider != HeaderProviderID {
		return nil, apperr.Invalid("credentials.errors.header_template_needed", "provider", provider)
	}
	return StaticHeaders(kind, payload)
}
