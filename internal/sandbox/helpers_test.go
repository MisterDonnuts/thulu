package sandbox

import (
	"testing"
	"time"
)

func TestEncodePorts_Nil(t *testing.T) {
	out, err := encodePorts(nil)
	if err != nil {
		t.Fatalf("encodePorts(nil): %v", err)
	}
	if out != "{}" {
		t.Errorf("encodePorts(nil) = %q, want {}", out)
	}
}

func TestEncodePorts_Roundtrip(t *testing.T) {
	m := map[int]int{3000: 30001, 5173: 30002, 8080: 31000}
	enc, err := encodePorts(m)
	if err != nil {
		t.Fatalf("encodePorts: %v", err)
	}

	got, err := decodePorts(enc)
	if err != nil {
		t.Fatalf("decodePorts: %v", err)
	}
	if len(got) != len(m) {
		t.Fatalf("roundtrip: len(got) = %d, want %d", len(got), len(m))
	}
	for k, v := range m {
		if got[k] != v {
			t.Errorf("roundtrip: port %d: got %d, want %d", k, got[k], v)
		}
	}
}

func TestDecodePorts_Empty(t *testing.T) {
	for _, s := range []string{"", "{}"} {
		got, err := decodePorts(s)
		if err != nil {
			t.Fatalf("decodePorts(%q): %v", s, err)
		}
		if len(got) != 0 {
			t.Errorf("decodePorts(%q): want empty map, got %v", s, got)
		}
	}
}

func TestDecodePorts_InvalidJSON(t *testing.T) {
	_, err := decodePorts("not-json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestNullableTime_Nil(t *testing.T) {
	got := nullableTime(nil)
	if got != nil {
		t.Errorf("nullableTime(nil) = %v, want nil", got)
	}
}

func TestNullableTime_NonNil(t *testing.T) {
	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	got := nullableTime(&ts)
	if got == nil {
		t.Error("nullableTime(non-nil) = nil, want time.Time")
	}
	if gotTime, ok := got.(time.Time); !ok || !gotTime.Equal(ts) {
		t.Errorf("nullableTime: got %v, want %v", got, ts)
	}
}
