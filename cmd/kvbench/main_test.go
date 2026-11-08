package main

import (
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	s := make([]time.Duration, 1000) // 1ms..1000ms
	for i := range s {
		s[i] = time.Duration(i+1) * time.Millisecond
	}
	for _, tc := range []struct {
		p    float64
		want time.Duration
	}{{50, 500 * time.Millisecond}, {99, 990 * time.Millisecond}, {99.9, 999 * time.Millisecond}, {100, time.Second}, {0, time.Millisecond}} {
		if got := percentile(s, tc.p); got != tc.want {
			t.Errorf("percentile(%v) = %v, want %v", tc.p, got, tc.want)
		}
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty = %v", got)
	}
}
