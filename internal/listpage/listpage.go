// Keyset, not OFFSET: lists order by timestamps that change (updated_at on every card drag), so
// offsets skip and repeat rows.
package listpage

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const MaxLimit = 500

type Request struct {
	Limit  int
	Cursor string
}

func (r Request) Clamp(ceiling int) Request {
	if r.Limit > ceiling {
		r.Limit = ceiling
	}
	return r
}

// ID breaks ties: timestamps are not unique, and comparing (At, ID) is what lets `<` skip and
// repeat nothing.
type Key struct {
	At time.Time
	ID string
}

// Answer 400; silently restarting at page one would loop a paging client over the first page.
var ErrBadCursor error = apperr.Invalid("listpage.errors.invalid_cursor")

func Encode(k Key) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(strconv.FormatInt(k.At.UTC().UnixNano(), 10) + "|" + k.ID))
}

func Decode(token string) (Key, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Key{}, ErrBadCursor
	}
	nanos, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return Key{}, ErrBadCursor
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return Key{}, ErrBadCursor
	}
	return Key{At: time.Unix(0, n).UTC(), ID: id}, nil
}
