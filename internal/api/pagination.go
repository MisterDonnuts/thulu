package api

import (
	"net/http"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/listpage"
)

// Cursor goes in a header so a paginated response stays the same bare JSON array existing clients
// read.
const nextCursorHeader = "X-Next-Cursor"

// A bad cursor is a 400, never a restart at page one: a paging client would loop over the first
// page forever.
func pageFromQuery(w http.ResponseWriter, r *http.Request) (listpage.Request, bool) {
	q := r.URL.Query()
	var p listpage.Request
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		p.Limit = n
	}
	p = p.Clamp(listpage.MaxLimit)

	if c := q.Get("cursor"); c != "" {
		if _, err := listpage.Decode(c); err != nil {
			writeErrKey(w, r, http.StatusBadRequest, "api.server.errors.invalid_cursor")
			return listpage.Request{}, false
		}
		p.Cursor = c
	}
	return p, true
}

// A short or empty page can still have a successor because workspace and subtask filters run in Go
// after the rows are read; clients page until the header disappears.
func setNextCursor(w http.ResponseWriter, cursor string) {
	if cursor != "" {
		w.Header().Set(nextCursorHeader, cursor)
	}
}
