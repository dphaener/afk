// Package activity holds the counting behind the home screen's LAST 24H
// column and THIS WEEK chart (T16.8). The counts are pure functions over log
// lines handed in, so the dashboard's View stays free of I/O; the only impure
// part is Tail in tail.go, which exists because the logs are append-only and
// grow without bound, so a reader must not take the whole file.
//
// Only the fields the counts need are decoded ("at", and "weighted" for the
// week). No log gains a field and neither decisionlog nor tokens is imported,
// so this package cannot drift their schemas.
package activity

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

const (
	// Buckets is the number of cells in the LAST 24H line.
	Buckets = 12
	// BucketSpan is the time one cell covers.
	BucketSpan = 2 * time.Hour
	// MaxRead caps how much of the end of a log is read.
	MaxRead = 1 << 20
)

// atOnly decodes just the timestamp of a log line.
type atOnly struct {
	At string `json:"at"`
}

// weighted decodes the timestamp and token weight of a ledger line.
type weighted struct {
	At       string  `json:"at"`
	Weighted float64 `json:"weighted"`
}

func parseAt(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Count buckets lines by their "at" time into Buckets cells of BucketSpan
// each, cell 0 the oldest and the last the two hours ending at now. A line
// belongs to cell i when now-24h+i*2h < at <= now-24h+(i+1)*2h; lines that do
// not decode, have no usable "at", are after now or at or before now-24h are
// skipped, as are blank lines.
func Count(lines []string, now time.Time) [Buckets]int {
	var out [Buckets]int
	start := now.Add(-Buckets * BucketSpan)
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var a atOnly
		if json.Unmarshal([]byte(l), &a) != nil {
			continue
		}
		t, ok := parseAt(a.At)
		if !ok || t.After(now) || !t.After(start) {
			continue
		}
		i := int((t.Sub(start) - 1) / BucketSpan)
		if t.Sub(start) <= 0 {
			continue
		}
		if i < 0 {
			i = 0
		}
		if i >= Buckets {
			i = Buckets - 1
		}
		out[i]++
	}
	return out
}

// Week is the token weight per local calendar day for the last seven days.
type Week struct {
	// Days[i] is the local midnight starting day i; Days[6] is today.
	Days [7]time.Time
	// Sums[i] is the summed weight of day i.
	Sums [7]float64
}

// WeekSums adds each line's "weighted" to the local day its "at" falls in,
// over the seven calendar days in loc ending with today. Days come from
// time.Date and AddDate rather than 24h steps so a daylight-saving day is the
// right length. Undecodable lines, lines outside the seven days and lines
// after now are skipped.
func WeekSums(lines []string, now time.Time, loc *time.Location) Week {
	var w Week
	y, m, d := now.In(loc).Date()
	for i := 0; i < 7; i++ {
		w.Days[i] = time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, i-6)
	}
	end := w.Days[6].AddDate(0, 0, 1)
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var p weighted
		if json.Unmarshal([]byte(l), &p) != nil {
			continue
		}
		t, ok := parseAt(p.At)
		if !ok || t.After(now) || t.Before(w.Days[0]) || !t.Before(end) {
			continue
		}
		for i := 6; i >= 0; i-- {
			if !t.Before(w.Days[i]) {
				w.Sums[i] += p.Weighted
				break
			}
		}
	}
	return w
}

// Add adds another project's sums into w. Both must come from WeekSums with
// the same now and loc, so Days line up; w.Days is kept as it is.
func (w *Week) Add(o Week) {
	for i := range w.Sums {
		w.Sums[i] += o.Sums[i]
	}
}

// Level is the glyph height for count against the busiest count: 0 for none,
// otherwise ceil(7*count/busiest) clamped to 1..7, so any activity shows.
func Level(count, busiest int) int {
	if count <= 0 || busiest <= 0 {
		return 0
	}
	l := int(math.Ceil(7 * float64(count) / float64(busiest)))
	if l < 1 {
		l = 1
	}
	if l > 7 {
		l = 7
	}
	return l
}

// Busiest is the largest cell across every project, the scale all rows share.
func Busiest(all ...[Buckets]int) int {
	b := 0
	for _, a := range all {
		for _, c := range a {
			if c > b {
				b = c
			}
		}
	}
	return b
}
