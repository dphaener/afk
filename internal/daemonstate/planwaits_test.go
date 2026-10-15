package daemonstate

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestPlanWaitsRoundTrip: the empty-list row is load-bearing because a nil or
// empty list must come back as [] on disk and as a non-nil slice to the
// reader, not null; the keys-always-present row pins that a zero field is
// still written, so a reader never guesses at absence.
func TestPlanWaitsRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		waits []PlanWait
		want  string
	}{
		{
			name:  "one wait",
			waits: []PlanWait{{Root: "/x/proj", Key: "k1", At: "2026-10-14T10:00:00Z"}},
			want:  `{"waits":[{"root":"/x/proj","key":"k1","at":"2026-10-14T10:00:00Z"}]}`,
		},
		{
			name:  "empty list encodes as []",
			waits: []PlanWait{},
			want:  `{"waits":[]}`,
		},
		{
			name:  "nil list encodes as []",
			waits: nil,
			want:  `{"waits":[]}`,
		},
		{
			name:  "zero fields are still written",
			waits: []PlanWait{{Root: "/x/proj"}},
			want:  `{"waits":[{"root":"/x/proj","key":"","at":""}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WritePlanWaits(dir, tt.waits); err != nil {
				t.Fatalf("WritePlanWaits: %v", err)
			}
			data, err := os.ReadFile(PlanWaitsPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(data)); got != tt.want {
				t.Errorf("file = %s, want %s", got, tt.want)
			}
			got := ReadPlanWaits(dir)
			if got == nil {
				t.Fatal("ReadPlanWaits returned nil, want a non-nil slice")
			}
			wantBack := tt.waits
			if len(wantBack) == 0 {
				wantBack = []PlanWait{}
			}
			if !reflect.DeepEqual(got, wantBack) {
				t.Errorf("ReadPlanWaits = %+v, want %+v", got, wantBack)
			}
		})
	}
}

// TestPlanWaitsDamaged: the missing and damaged rows are load-bearing because
// a read that returned an error or nil here would stop the daemon's pass on
// the first bad byte; each must read as an empty, non-nil list. The
// wrong-shape row pins that a JSON file that parses but is not this shape
// does not invent entries.
func TestPlanWaitsDamaged(t *testing.T) {
	tests := []struct {
		name string
		body *string
	}{
		{name: "missing", body: nil},
		{name: "damaged json", body: strPtr(`{"waits":[`)},
		{name: "empty file", body: strPtr(``)},
		{name: "wrong shape", body: strPtr(`[1,2,3]`)},
		{name: "null list", body: strPtr(`{"waits":null}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.body != nil {
				if err := os.WriteFile(PlanWaitsPath(dir), []byte(*tt.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := ReadPlanWaits(dir)
			if got == nil {
				t.Fatal("ReadPlanWaits returned nil, want an empty non-nil slice")
			}
			if len(got) != 0 {
				t.Errorf("ReadPlanWaits = %+v, want empty", got)
			}
		})
	}
}

// TestPlanWaitsOverwrite: the row is load-bearing because the daemon replaces
// the whole list on each notification; a write that appended or left the old
// entries would re-notify a wait the owner already saw.
func TestPlanWaitsOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := WritePlanWaits(dir, []PlanWait{{Root: "/a", Key: "1", At: "t1"}}); err != nil {
		t.Fatal(err)
	}
	if err := WritePlanWaits(dir, []PlanWait{{Root: "/b", Key: "2", At: "t2"}}); err != nil {
		t.Fatal(err)
	}
	got := ReadPlanWaits(dir)
	want := []PlanWait{{Root: "/b", Key: "2", At: "t2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after overwrite = %+v, want %+v", got, want)
	}
	data, err := os.ReadFile(PlanWaitsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("file is not JSON: %v", err)
	}
	if len(f) != 1 {
		t.Errorf("file has keys %v, want only waits", f)
	}
}
