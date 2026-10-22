package activity

import (
	"fmt"
	"testing"
	"time"
)

var now0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func ln(t time.Time) string {
	return fmt.Sprintf(`{"at":%q,"weighted":1}`, t.Format(time.RFC3339Nano))
}

func rep(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// TestCount: the edge rows are load-bearing: a line exactly at now-24h is out,
// exactly at a bucket's upper edge is in that bucket, and exactly now is in
// the last one. Garbage and torn lines must be skipped, not crash or count.
func TestCount(t *testing.T) {
	start := now0.Add(-24 * time.Hour)
	var busy [Buckets]int
	busy[5] = 40
	cases := []struct {
		name  string
		lines []string
		want  [Buckets]int
	}{
		{"empty log", nil, [Buckets]int{}},
		{"all older than 24 hours", []string{ln(now0.Add(-25 * time.Hour)), ln(now0.Add(-48 * time.Hour))}, [Buckets]int{}},
		{"busy hour", rep(ln(start.Add(11*time.Hour+30*time.Minute)), 40), busy},
		{"bucket edges", []string{ln(start), ln(start.Add(time.Nanosecond)), ln(start.Add(2 * time.Hour)), ln(start.Add(2*time.Hour + time.Nanosecond)), ln(now0)}, func() [Buckets]int {
			var b [Buckets]int
			b[0] = 2
			b[1] = 1
			b[11] = 1
			return b
		}()},
		{"future line", []string{ln(now0.Add(time.Second))}, [Buckets]int{}},
		{"garbage line", []string{"not json", `{"at":"nope"}`, `{"x":1}`, ""}, [Buckets]int{}},
		{"torn last line", []string{ln(now0.Add(-time.Hour)), `{"at":"2026-10-22T11:`}, func() [Buckets]int {
			var b [Buckets]int
			b[11] = 1
			return b
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Count(c.lines, now0); got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func wl(at string, w float64) string {
	return fmt.Sprintf(`{"at":%q,"weighted":%v}`, at, w)
}

// TestWeekSums: the non-UTC row is load-bearing: a UTC stamp just after
// midnight UTC is still the previous day in New York, and counting by UTC day
// would put the bar on the wrong day.
func TestWeekSums(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	cases := []struct {
		name  string
		lines []string
		loc   *time.Location
		want  [7]float64
	}{
		{"empty", nil, time.UTC, [7]float64{}},
		{"a line per day", []string{
			wl("2026-10-16T10:00:00Z", 1), wl("2026-10-17T10:00:00Z", 2), wl("2026-10-18T10:00:00Z", 3),
			wl("2026-10-19T10:00:00Z", 4), wl("2026-10-20T10:00:00Z", 5), wl("2026-10-21T10:00:00Z", 6),
			wl("2026-10-22T10:00:00Z", 7)}, time.UTC, [7]float64{1, 2, 3, 4, 5, 6, 7}},
		{"two lines same day", []string{wl("2026-10-22T01:00:00Z", 1.5), wl("2026-10-22T09:00:00Z", 2)}, time.UTC, [7]float64{0, 0, 0, 0, 0, 0, 3.5}},
		{"eight days old", []string{wl("2026-10-14T10:00:00Z", 9), wl("2026-10-15T23:59:59Z", 9)}, time.UTC, [7]float64{}},
		{"future line", []string{wl("2026-10-22T12:00:01Z", 9)}, time.UTC, [7]float64{}},
		{"garbage line", []string{"nope", `{"at":"x","weighted":3}`, ""}, time.UTC, [7]float64{}},
		{"previous local day", []string{wl("2026-10-22T02:00:00Z", 4)}, ny, [7]float64{0, 0, 0, 0, 0, 4, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := WeekSums(c.lines, now0, c.loc)
			if w.Sums != c.want {
				t.Errorf("got %v want %v", w.Sums, c.want)
			}
			if !w.Days[6].Equal(time.Date(2026, 10, 22, 0, 0, 0, 0, c.loc)) {
				t.Errorf("today = %v", w.Days[6])
			}
		})
	}
}

func TestWeekAdd(t *testing.T) {
	a := WeekSums([]string{wl("2026-10-22T10:00:00Z", 1)}, now0, time.UTC)
	b := WeekSums([]string{wl("2026-10-22T11:00:00Z", 2), wl("2026-10-21T11:00:00Z", 5)}, now0, time.UTC)
	a.Add(b)
	if a.Sums[6] != 3 || a.Sums[5] != 5 {
		t.Errorf("sums = %v", a.Sums)
	}
}

// TestLevel: a single event against a huge busiest must still show (1), and
// the busiest itself is 7; zero is the only way to get 0.
func TestLevel(t *testing.T) {
	cases := []struct {
		name           string
		count, busiest int
		want           int
	}{
		{"zero count", 0, 10, 0},
		{"zero busiest", 3, 0, 0},
		{"negative busiest", 3, -1, 0},
		{"one of many", 1, 1000, 1},
		{"half", 5, 10, 4},
		{"busiest", 10, 10, 7},
		{"over busiest clamps", 20, 10, 7},
		{"one of one", 1, 1, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Level(c.count, c.busiest); got != c.want {
				t.Errorf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestBusiest(t *testing.T) {
	a := [Buckets]int{1, 2, 3}
	b := [Buckets]int{0, 9}
	cases := []struct {
		name string
		in   [][Buckets]int
		want int
	}{
		{"none", nil, 0},
		{"one", [][Buckets]int{a}, 3},
		{"across projects", [][Buckets]int{a, b}, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Busiest(c.in...); got != c.want {
				t.Errorf("got %d want %d", got, c.want)
			}
		})
	}
}
