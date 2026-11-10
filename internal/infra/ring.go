package infra

// Not safe for concurrent use; the collector guards it.
type ring[T any] struct {
	buf  []T
	next int
	full bool
}

func newRing[T any](n int) *ring[T] {
	if n < 1 {
		n = 1
	}
	return &ring[T]{buf: make([]T, n)}
}

func (r *ring[T]) push(v T) {
	r.buf[r.next] = v
	r.next++
	if r.next == len(r.buf) {
		r.next = 0
		r.full = true
	}
}

func (r *ring[T]) len() int {
	if r.full {
		return len(r.buf)
	}
	return r.next
}

func (r *ring[T]) values() []T {
	out := make([]T, 0, r.len())
	if r.full {
		out = append(out, r.buf[r.next:]...)
	}
	return append(out, r.buf[:r.next]...)
}

func (r *ring[T]) last() (T, bool) {
	var zero T
	if r.len() == 0 {
		return zero, false
	}
	i := r.next - 1
	if i < 0 {
		i = len(r.buf) - 1
	}
	return r.buf[i], true
}
