package infra

import (
	"slices"
	"testing"
)

func TestRingKeepsNewestInOrder(t *testing.T) {
	r := newRing[int](3)
	if _, ok := r.last(); ok || r.len() != 0 || len(r.values()) != 0 {
		t.Fatal("a new ring is not empty")
	}
	r.push(1)
	r.push(2)
	if got := r.values(); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("before wrapping = %v", got)
	}
	for _, v := range []int{3, 4, 5} {
		r.push(v)
	}
	if got := r.values(); !slices.Equal(got, []int{3, 4, 5}) {
		t.Errorf("after wrapping = %v, want the newest three oldest first", got)
	}
	if v, ok := r.last(); !ok || v != 5 {
		t.Errorf("last = %v %v", v, ok)
	}
	if r.len() != 3 {
		t.Errorf("len = %d", r.len())
	}
	got := r.values()
	got[0] = 99
	if r.values()[0] != 3 {
		t.Error("values aliases the ring's buffer")
	}
	r.push(6)
	if v, _ := r.last(); v != 6 {
		t.Errorf("last after a push at index 0 = %v", v)
	}
}

func TestRingMinimumSize(t *testing.T) {
	r := newRing[string](0)
	r.push("a")
	r.push("b")
	if got := r.values(); !slices.Equal(got, []string{"b"}) {
		t.Errorf("size-0 ring = %v, want it clamped to one slot", got)
	}
}
