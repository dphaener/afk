package controlroom

import (
	"testing"
	"time"
)

// Load-bearing rows: "afk backend" and "bad config" (an afk-only machine
// must never reach for Rex), "server not answering" (no start into a dead
// server), "started under a minute ago" (the loop guard) against "started a
// minute or more ago" (the boundary where it may start again), and "session
// present but window dead" (the case the keep-alive exists for).
func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ok := Facts{Backend: "rex", ServerAnswers: true, Now: now}
	with := func(f func(*Facts)) Facts { c := ok; f(&c); return c }

	tests := []struct {
		name string
		in   Facts
		want Kind
	}{
		{"afk backend", with(func(f *Facts) { f.Backend = "afk" }), Nothing},
		{"bad config", with(func(f *Facts) { f.ConfigErr = true }), Nothing},
		{"server not answering", with(func(f *Facts) { f.ServerAnswers = false }), Nothing},
		{"session and window alive", with(func(f *Facts) { f.SessionFound, f.WindowAlive = true, true }), Nothing},
		{"started under a minute ago", with(func(f *Facts) { f.LastStart = now.Add(-59 * time.Second) }), Wait},
		{"started a minute or more ago", with(func(f *Facts) { f.LastStart = now.Add(-time.Minute) }), Start},
		{"never started", ok, Start},
		{"session present but window dead", with(func(f *Facts) { f.SessionFound = true }), Start},
		{"session missing", with(func(f *Facts) { f.WindowAlive = true }), Start},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.in)
			if got.Kind != tc.want {
				t.Errorf("Decide = %v (%s), want %v", got.Kind, got.Reason, tc.want)
			}
			if got.Reason == "" {
				t.Error("empty reason")
			}
		})
	}
}
