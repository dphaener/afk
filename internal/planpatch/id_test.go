package planpatch

import (
	"strings"
	"testing"
)

// TestNextInMilestone is the choice of the next number in a milestone. The
// load-bearing row is "a gap in the numbers": the answer is the highest
// number plus one, never the free number lower down, because a missing
// number is a task that was taken out of use and its id is never given to
// other work (rule 8). "letters do not count as numbers" guards the other
// way round: T2.2a must not push the next id to T2.4.
func TestNextInMilestone(t *testing.T) {
	tests := []struct {
		name      string
		ids       []string
		milestone int
		want      string
		// wantErr is a piece of the error message wanted; "" means no error.
		wantErr string
	}{
		{name: "a milestone with tasks", ids: []string{"T1.1", "T2.1", "T2.2", "T3.1"}, milestone: 2, want: "T2.3"},
		{name: "letters do not count as numbers", ids: []string{"T2.1", "T2.2", "T2.2a", "T2.2b"}, milestone: 2, want: "T2.3"},
		{name: "the highest number has a letter", ids: []string{"T2.1", "T2.4c"}, milestone: 2, want: "T2.5"},
		{name: "an empty milestone", ids: []string{"T1.1", "T1.2"}, milestone: 3, want: "T3.1"},
		{name: "no ids at all", ids: nil, milestone: 1, want: "T1.1"},
		{name: "a gap in the numbers", ids: []string{"T2.1", "T2.2", "T2.5"}, milestone: 2, want: "T2.6"},
		{name: "ids out of order", ids: []string{"T2.7", "T2.1", "T2.3"}, milestone: 2, want: "T2.8"},
		{name: "milestone 0, whose first task is numbered 0", ids: []string{"T0.0"}, milestone: 0, want: "T0.1"},
		{name: "another milestone with the same digits is not counted", ids: []string{"T12.9", "T1.2"}, milestone: 1, want: "T1.3"},
		{name: "text that is not an id is passed over", ids: []string{"T2.1", "t2.9", "T2.x", ""}, milestone: 2, want: "T2.2"},
		{name: "a milestone below 0", ids: []string{"T1.1"}, milestone: -1, wantErr: "not a milestone number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextInMilestone(tt.ids, tt.milestone)
			checkID(t, got, err, tt.want, tt.wantErr)
		})
	}
}

// TestBetween is the choice of a letter for a task that runs between two
// others. The load-bearing row is "a suffix already taken": the letter must
// be the first free one, or the new task would take the id of a task the
// plan already has. "after an id that has a letter" and "a later letter is
// taken" hold the other half: only letters later than the named task's own
// are tried, so the new id sorts where the task runs.
func TestBetween(t *testing.T) {
	var all []string
	for c := 'a'; c <= 'z'; c++ {
		all = append(all, "T2.2"+string(c))
	}
	all = append(all, "T2.2")

	tests := []struct {
		name    string
		ids     []string
		after   string
		want    string
		wantErr string
	}{
		{name: "a letter on a plain id", ids: []string{"T2.1", "T2.2", "T2.3"}, after: "T2.2", want: "T2.2a"},
		{name: "a suffix already taken", ids: []string{"T2.2", "T2.2a", "T2.3"}, after: "T2.2", want: "T2.2b"},
		{name: "two suffixes already taken", ids: []string{"T2.2", "T2.2a", "T2.2b"}, after: "T2.2", want: "T2.2c"},
		{name: "after an id that has a letter", ids: []string{"T2.2", "T2.2a"}, after: "T2.2a", want: "T2.2b"},
		{name: "a later letter is taken", ids: []string{"T2.2", "T2.2a", "T2.2b"}, after: "T2.2a", want: "T2.2c"},
		{name: "an earlier free letter is not used", ids: []string{"T2.2", "T2.2c"}, after: "T2.2c", want: "T2.2d"},
		{name: "another task's letters do not count", ids: []string{"T2.2", "T2.3", "T2.3a"}, after: "T2.2", want: "T2.2a"},
		{name: "every letter taken", ids: all, after: "T2.2", wantErr: "next-id --milestone 2"},
		{name: "after the last letter", ids: []string{"T2.2", "T2.2z"}, after: "T2.2z", wantErr: "every letter after T2.2z is taken"},
		{name: "an unknown task", ids: []string{"T2.1", "T2.2"}, after: "T2.9", wantErr: "no task T2.9"},
		{name: "an invalid id", ids: []string{"T2.1"}, after: "2.1", wantErr: "not a task id"},
		{name: "an id with two letters", ids: []string{"T2.1"}, after: "T2.1ab", wantErr: "not a task id"},
		{name: "an empty id", ids: []string{"T2.1"}, after: "", wantErr: "not a task id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Between(tt.ids, tt.after)
			checkID(t, got, err, tt.want, tt.wantErr)
		})
	}
}

func checkID(t *testing.T, got string, err error, want, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil {
			t.Fatalf("got %q, want an error holding %q", got, wantErr)
		}
		if !strings.Contains(err.Error(), wantErr) {
			t.Errorf("error = %q, want it to hold %q", err, wantErr)
		}
		if got != "" {
			t.Errorf("got %q with an error, want no id", got)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
