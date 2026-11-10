package apperr

import (
	"encoding/json"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type Body struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func BodyFor(r *http.Request, err error) Body {
	return Body{Error: Localize(err, i18n.RequestLocale(r)), Code: Code(err)}
}

func WriteJSON(w http.ResponseWriter, r *http.Request, status int, err error) {
	if status == 0 {
		status = Status(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(BodyFor(r, err))
}
