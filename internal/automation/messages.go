package automation

import (
	"encoding/json"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// The plain text columns keep the daemon-locale rendering for readers that predate the per-locale
// columns.
type Msg map[string]string

func msgText(key string, args ...any) Msg {
	m := Msg{}
	for _, l := range i18n.Default().Locales() {
		m[l] = i18n.T(l, key, args...)
	}
	return m
}

func msgErr(err error) Msg {
	if err == nil {
		return nil
	}
	m := Msg{}
	for _, l := range i18n.Default().Locales() {
		m[l] = apperr.Localize(err, l)
	}
	return m
}

func (m Msg) In(locale string) string {
	if v, ok := m[locale]; ok {
		return v
	}
	return m[i18n.SourceLocale]
}

func (m Msg) Text() string {
	if len(m) == 0 {
		return ""
	}
	return m.In(i18n.DaemonLocale())
}

func (m Msg) encode() string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(map[string]string(m))
	if err != nil {
		return ""
	}
	return string(b)
}

// Rows written before the column existed hold "" and decode to nil, so Localize keeps their text.
func decodeMsg(s string) Msg {
	if s == "" {
		return nil
	}
	var m Msg
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}

func localized(m Msg, locale, fallback string) string {
	if len(m) == 0 {
		return fallback
	}
	return m.In(locale)
}

func (r *Run) Localize(locale string) {
	r.Reason = localized(r.ReasonMsg, locale, r.Reason)
	r.Error = localized(r.ErrorMsg, locale, r.Error)
	for i := range r.Steps {
		st := &r.Steps[i]
		st.Output = localized(st.OutputMsg, locale, st.Output)
		st.Error = localized(st.ErrorMsg, locale, st.Error)
	}
}
