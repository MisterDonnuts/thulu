package apperr

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type Kind uint8

const (
	KindUnspecified Kind = iota
	KindInvalid
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindPrecondition
	KindTooLarge
	KindUnprocessable
	KindRateLimited
	KindInternal
	KindNotImplemented
	KindUnavailable
	KindTimeout
)

var kindStatus = map[Kind]int{
	KindInvalid:         http.StatusBadRequest,
	KindUnauthenticated: http.StatusUnauthorized,
	KindForbidden:       http.StatusForbidden,
	KindNotFound:        http.StatusNotFound,
	KindConflict:        http.StatusConflict,
	KindPrecondition:    http.StatusPreconditionFailed,
	KindTooLarge:        http.StatusRequestEntityTooLarge,
	KindUnprocessable:   http.StatusUnprocessableEntity,
	KindRateLimited:     http.StatusTooManyRequests,
	KindInternal:        http.StatusInternalServerError,
	KindNotImplemented:  http.StatusNotImplemented,
	KindUnavailable:     http.StatusServiceUnavailable,
	KindTimeout:         http.StatusGatewayTimeout,
}

func (k Kind) HTTPStatus() int {
	if s, ok := kindStatus[k]; ok {
		return s
	}
	return http.StatusInternalServerError
}

type Error struct {
	Kind Kind
	Key  string
	Args []any
	Err  error
}

func (e *Error) Error() string { return Localize(e, i18n.SourceLocale) }

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Key == e.Key
}

func (e *Error) WithKind(k Kind) *Error {
	c := *e
	c.Kind = k
	return &c
}

func newErr(k Kind, key string, args []any) *Error { return &Error{Kind: k, Key: key, Args: args} }

func New(key string, args ...any) *Error { return newErr(KindUnspecified, key, args) }

func Invalid(key string, args ...any) *Error { return newErr(KindInvalid, key, args) }

func Unauthenticated(key string, args ...any) *Error {
	return newErr(KindUnauthenticated, key, args)
}

func Forbidden(key string, args ...any) *Error { return newErr(KindForbidden, key, args) }

func NotFound(key string, args ...any) *Error { return newErr(KindNotFound, key, args) }

func Conflict(key string, args ...any) *Error { return newErr(KindConflict, key, args) }

func Precondition(key string, args ...any) *Error { return newErr(KindPrecondition, key, args) }

func TooLarge(key string, args ...any) *Error { return newErr(KindTooLarge, key, args) }

func Unprocessable(key string, args ...any) *Error { return newErr(KindUnprocessable, key, args) }

func RateLimited(key string, args ...any) *Error { return newErr(KindRateLimited, key, args) }

func Internal(key string, args ...any) *Error { return newErr(KindInternal, key, args) }

func NotImplemented(key string, args ...any) *Error {
	return newErr(KindNotImplemented, key, args)
}

func Unavailable(key string, args ...any) *Error { return newErr(KindUnavailable, key, args) }

func Timeout(key string, args ...any) *Error { return newErr(KindTimeout, key, args) }

func Wrap(err error, key string, args ...any) error {
	if err == nil {
		return nil
	}
	return &Error{Key: key, Args: args, Err: err}
}

func WrapKind(k Kind, err error, key string, args ...any) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: k, Key: key, Args: args, Err: err}
}

func KindOf(err error) Kind {
	var e *Error
	for errors.As(err, &e) {
		if e.Kind != KindUnspecified {
			return e.Kind
		}
		err = e.Err
	}
	return KindInternal
}

func Status(err error) int { return KindOf(err).HTTPStatus() }

func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Key
	}
	return ""
}

// Reserved: Localize fills {cause} with the wrapped error; callers never pass it.
const CauseArg = "cause"

func Localize(err error, locale string) string {
	var parts []string
	for err != nil {
		//nolint:errorlint // one layer at a time on purpose: errors.As would jump over a foreign layer whose text belongs in the message
		if e, ok := err.(*Error); ok {
			if e.Err != nil && i18n.UsesCause(locale, e.Key) {
				parts = append(parts, i18n.T(locale, e.Key, append(localizeArgs(e.Args, locale), CauseArg, Localize(e.Err, locale))...))
				break
			}
			parts = append(parts, i18n.T(locale, e.Key, localizeArgs(e.Args, locale)...))
			err = e.Err
			continue
		}
		if u := errors.Unwrap(err); u != nil && err.Error() == u.Error() {
			err = u
			continue
		}
		parts = append(parts, err.Error())
		break
	}
	return strings.Join(parts, ": ")
}

func localizeArgs(args []any, locale string) []any {
	var out []any // Copy on first write: e.Args is shared and must not be mutated.
	for i := 1; i < len(args); i += 2 {
		var text string
		switch v := args[i].(type) {
		case List:
			text = v.localize(locale)
		case error:
			text = Localize(v, locale)
		default:
			continue
		}
		if out == nil {
			out = append([]any(nil), args...)
		}
		out[i] = text
	}
	if out == nil {
		return args
	}
	return out
}

type List struct {
	Errs []error
	Sep  string
}

func (l List) Error() string { return l.localize(i18n.SourceLocale) }

func (l List) localize(locale string) string {
	parts := make([]string, 0, len(l.Errs))
	for _, e := range l.Errs {
		if e != nil {
			parts = append(parts, Localize(e, locale))
		}
	}
	return strings.Join(parts, l.Sep)
}

func LocalizeCtx(ctx context.Context, err error) string {
	return Localize(err, i18n.FromContext(ctx))
}

func Text(err error) string { return Localize(err, i18n.DaemonLocale()) }
