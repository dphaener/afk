package daemonstate

import (
	"os"
	"reflect"
	"testing"
)

// reviewAdd is one call to AddReview in a table row.
type reviewAdd struct {
	root string
	at   string
}

// TestReviewsDamagedFile: the rows are load-bearing because a missing file, an
// empty file, a file that is not JSON and a file of the wrong shape must all
// read as an empty list with no error, or the page cannot load. The last
// check is that the next add writes a good file over the damaged one, so one
// bad write never blocks the review list for good.
func TestReviewsDamagedFile(t *testing.T) {
	tests := []struct {
		name    string
		content *string
	}{
		{"missing file", nil},
		{"empty file", strPtr("")},
		{"not JSON", strPtr("{reviews:")},
		{"wrong shape", strPtr(`{"reviews":"yes"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content != nil {
				if err := os.WriteFile(ReviewsPath(dir), []byte(*tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := ReadReviews(dir); len(got) != 0 {
				t.Fatalf("ReadReviews = %+v, want empty", got)
			}
			if err := AddReview(dir, Review{Root: "/x/proj", At: "2026-10-13T12:00:00Z"}); err != nil {
				t.Fatalf("AddReview after a damaged file: %v", err)
			}
			got := ReadReviews(dir)
			want := []Review{{Name: "proj", Root: "/x/proj", At: "2026-10-13T12:00:00Z"}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("after the next add: got %+v, want %+v", got, want)
			}
		})
	}
}

// TestReviewsAdd: the rows are load-bearing for the three rules of the list.
// "add twice" keeps the entry's name, so the page's address for a plan does
// not move when the plan goes back up for review; the two-folders rows show
// that a second folder with the same name gets -2 and -3 rather than hiding
// the first; the trailing-slash row shows that a root is keyed by its cleaned
// path. The time with an offset shows that At is stored in UTC.
func TestReviewsAdd(t *testing.T) {
	tests := []struct {
		name string
		adds []reviewAdd
		want []Review
	}{
		{
			name: "add once",
			adds: []reviewAdd{{"/x/proj", "2026-10-13T12:00:00Z"}},
			want: []Review{{Name: "proj", Root: "/x/proj", At: "2026-10-13T12:00:00Z"}},
		},
		{
			name: "add twice replaces the time and keeps the name",
			adds: []reviewAdd{
				{"/x/proj", "2026-10-13T12:00:00Z"},
				{"/x/proj", "2026-10-14T09:30:00Z"},
			},
			want: []Review{{Name: "proj", Root: "/x/proj", At: "2026-10-14T09:30:00Z"}},
		},
		{
			name: "same root with a trailing slash is the same entry",
			adds: []reviewAdd{
				{"/x/proj/", "2026-10-13T12:00:00Z"},
				{"/x/proj", "2026-10-13T13:00:00Z"},
			},
			want: []Review{{Name: "proj", Root: "/x/proj", At: "2026-10-13T13:00:00Z"}},
		},
		{
			name: "two roots with one folder name get -2",
			adds: []reviewAdd{
				{"/a/app", "2026-10-13T12:00:00Z"},
				{"/b/app", "2026-10-13T12:05:00Z"},
			},
			want: []Review{
				{Name: "app", Root: "/a/app", At: "2026-10-13T12:00:00Z"},
				{Name: "app-2", Root: "/b/app", At: "2026-10-13T12:05:00Z"},
			},
		},
		{
			name: "three roots with one folder name get -2 and -3",
			adds: []reviewAdd{
				{"/a/app", "2026-10-13T12:00:00Z"},
				{"/b/app", "2026-10-13T12:00:00Z"},
				{"/c/app", "2026-10-13T12:00:00Z"},
			},
			want: []Review{
				{Name: "app", Root: "/a/app", At: "2026-10-13T12:00:00Z"},
				{Name: "app-2", Root: "/b/app", At: "2026-10-13T12:00:00Z"},
				{Name: "app-3", Root: "/c/app", At: "2026-10-13T12:00:00Z"},
			},
		},
		{
			name: "an offset time is stored in UTC",
			adds: []reviewAdd{{"/x/proj", "2026-10-13T08:00:00-04:00"}},
			want: []Review{{Name: "proj", Root: "/x/proj", At: "2026-10-13T12:00:00Z"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, a := range tt.adds {
				if err := AddReview(dir, Review{Root: a.root, At: a.at}); err != nil {
					t.Fatalf("AddReview(%q): %v", a.root, err)
				}
			}
			if got := ReadReviews(dir); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestReviewsAddRejects: the rows are load-bearing because a review with no
// folder or with a time that is not RFC 3339 would be written and then could
// never be served or sorted, so AddReview refuses it and writes nothing.
func TestReviewsAddRejects(t *testing.T) {
	tests := []struct {
		name string
		r    Review
	}{
		{"no folder", Review{At: "2026-10-13T12:00:00Z"}},
		{"time not RFC 3339", Review{Root: "/x/proj", At: "yesterday"}},
		{"no time", Review{Root: "/x/proj"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := AddReview(dir, tt.r); err == nil {
				t.Fatal("AddReview accepted a bad review")
			}
			if _, err := os.Stat(ReviewsPath(dir)); !os.IsNotExist(err) {
				t.Errorf("a refused add wrote the file (stat err %v)", err)
			}
		})
	}
}

// TestReviewsRemove: the rows are load-bearing because approve removes the
// entry on every run, so removing an absent root must succeed and write
// nothing, and removing one of two must leave the other alone. The
// trailing-slash row removes by the cleaned path, matching how the entry was
// added.
func TestReviewsRemove(t *testing.T) {
	tests := []struct {
		name   string
		adds   []reviewAdd
		remove string
		want   []Review
	}{
		{
			name:   "remove one of two",
			adds:   []reviewAdd{{"/a/app", "2026-10-13T12:00:00Z"}, {"/b/other", "2026-10-13T12:00:00Z"}},
			remove: "/a/app",
			want:   []Review{{Name: "other", Root: "/b/other", At: "2026-10-13T12:00:00Z"}},
		},
		{
			name:   "remove by a path with a trailing slash",
			adds:   []reviewAdd{{"/a/app", "2026-10-13T12:00:00Z"}},
			remove: "/a/app/",
			want:   nil,
		},
		{
			name:   "remove when absent leaves the list alone",
			adds:   []reviewAdd{{"/a/app", "2026-10-13T12:00:00Z"}},
			remove: "/nowhere",
			want:   []Review{{Name: "app", Root: "/a/app", At: "2026-10-13T12:00:00Z"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, a := range tt.adds {
				if err := AddReview(dir, Review{Root: a.root, At: a.at}); err != nil {
					t.Fatal(err)
				}
			}
			if err := RemoveReview(dir, tt.remove); err != nil {
				t.Fatalf("RemoveReview: %v", err)
			}
			if got := ReadReviews(dir); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestReviewsRemoveAbsentWritesNothing: the row is load-bearing because approve
// calls RemoveReview on every run, including runs where no review was ever
// listed (a plan approved from the command line, or a second run). Creating a
// file there would be a write for nothing, so the folder must stay empty.
func TestReviewsRemoveAbsentWritesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := RemoveReview(dir, "/x/proj"); err != nil {
		t.Fatalf("RemoveReview with no file: %v", err)
	}
	if _, err := os.Stat(ReviewsPath(dir)); !os.IsNotExist(err) {
		t.Errorf("RemoveReview with no file created one (stat err %v)", err)
	}
}

// TestReviewsFileBytes: the rows are load-bearing because the file's shape is
// what the web page and any other reader depend on. The one-entry row pins the
// exact bytes, with every key present and the time in UTC. The empty row pins
// that the list is written as an array, never null, after the last entry is
// removed.
func TestReviewsFileBytes(t *testing.T) {
	tests := []struct {
		name string
		do   func(t *testing.T, dir string)
		want string
	}{
		{
			name: "one entry",
			do: func(t *testing.T, dir string) {
				if err := AddReview(dir, Review{Root: "/x/proj", At: "2026-10-13T08:00:00-04:00"}); err != nil {
					t.Fatal(err)
				}
			},
			want: `{"reviews":[{"name":"proj","root":"/x/proj","at":"2026-10-13T12:00:00Z"}]}` + "\n",
		},
		{
			name: "emptied list",
			do: func(t *testing.T, dir string) {
				if err := AddReview(dir, Review{Root: "/x/proj", At: "2026-10-13T12:00:00Z"}); err != nil {
					t.Fatal(err)
				}
				if err := RemoveReview(dir, "/x/proj"); err != nil {
					t.Fatal(err)
				}
			},
			want: `{"reviews":[]}` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.do(t, dir)
			raw, err := os.ReadFile(ReviewsPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tt.want {
				t.Errorf("file bytes:\n got %q\nwant %q", raw, tt.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
