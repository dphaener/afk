package daemonstate

import (
	"os"
	"reflect"
	"testing"
)

// TestBeingPlannedAdd: the rows are load-bearing for the naming rules. "add
// again" must keep the name and only move the time, or the chat address moves
// under the owner; the two "taken" rows show a name used by a configured
// project or by another entry gets -2, so no two projects share an address.
// The bad-at rows show nothing is written for a refused add.
func TestBeingPlannedAdd(t *testing.T) {
	const t1, t2 = "2026-10-14T10:00:00Z", "2026-10-14T11:00:00Z"
	tests := []struct {
		name     string
		existing []BeingPlanned
		add      BeingPlanned
		taken    []string
		want     BeingPlanned
		wantErr  bool
		wantList []BeingPlanned
	}{
		{
			name:     "add a new root",
			add:      BeingPlanned{Root: "/x/proj/", At: t1},
			want:     BeingPlanned{Name: "proj", Root: "/x/proj", At: t1},
			wantList: []BeingPlanned{{Name: "proj", Root: "/x/proj", At: t1}},
		},
		{
			name:     "add again keeps the name and updates at",
			existing: []BeingPlanned{{Name: "mine", Root: "/x/proj", At: t1}},
			add:      BeingPlanned{Name: "other", Root: "/x/proj", At: t2},
			want:     BeingPlanned{Name: "mine", Root: "/x/proj", At: t2},
			wantList: []BeingPlanned{{Name: "mine", Root: "/x/proj", At: t2}},
		},
		{
			name:     "caller name is used",
			add:      BeingPlanned{Name: "chosen", Root: "/x/proj", At: t1},
			want:     BeingPlanned{Name: "chosen", Root: "/x/proj", At: t1},
			wantList: []BeingPlanned{{Name: "chosen", Root: "/x/proj", At: t1}},
		},
		{
			name:     "name taken by the caller gets -2",
			add:      BeingPlanned{Root: "/x/proj", At: t1},
			taken:    []string{"proj"},
			want:     BeingPlanned{Name: "proj-2", Root: "/x/proj", At: t1},
			wantList: []BeingPlanned{{Name: "proj-2", Root: "/x/proj", At: t1}},
		},
		{
			name:     "name taken by another entry gets -2",
			existing: []BeingPlanned{{Name: "proj", Root: "/a/proj", At: t1}},
			add:      BeingPlanned{Root: "/b/proj", At: t2},
			want:     BeingPlanned{Name: "proj-2", Root: "/b/proj", At: t2},
			wantList: []BeingPlanned{
				{Name: "proj", Root: "/a/proj", At: t1},
				{Name: "proj-2", Root: "/b/proj", At: t2},
			},
		},
		{
			name:     "taken twice gets -3",
			existing: []BeingPlanned{{Name: "proj-2", Root: "/a/proj", At: t1}},
			add:      BeingPlanned{Root: "/b/proj", At: t2},
			taken:    []string{"proj"},
			want:     BeingPlanned{Name: "proj-3", Root: "/b/proj", At: t2},
			wantList: []BeingPlanned{
				{Name: "proj-2", Root: "/a/proj", At: t1},
				{Name: "proj-3", Root: "/b/proj", At: t2},
			},
		},
		{
			name:    "bad at is refused",
			add:     BeingPlanned{Root: "/x/proj", At: "yesterday"},
			wantErr: true,
		},
		{
			name:    "empty root is refused",
			add:     BeingPlanned{At: t1},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.existing != nil {
				if err := writeBeingPlannedFile(dir, append([]BeingPlanned(nil), tt.existing...)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := AddBeingPlanned(dir, tt.add, tt.taken)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.existing == nil {
					if _, statErr := os.Stat(BeingPlannedPath(dir)); !os.IsNotExist(statErr) {
						t.Errorf("a refused add wrote the file (stat err %v)", statErr)
					}
				}
				return
			}
			if got != tt.want {
				t.Errorf("returned %+v, want %+v", got, tt.want)
			}
			if list := ReadBeingPlanned(dir); !reflect.DeepEqual(list, tt.wantList) {
				t.Errorf("list = %+v, want %+v", list, tt.wantList)
			}
		})
	}
}

// TestBeingPlannedRemove: the unlisted row is load-bearing because approve
// calls remove on every run, and it must not create or rewrite the file.
func TestBeingPlannedRemove(t *testing.T) {
	t.Run("listed root is removed", func(t *testing.T) {
		dir := t.TempDir()
		for _, r := range []string{"/a/one", "/a/two"} {
			if _, err := AddBeingPlanned(dir, BeingPlanned{Root: r, At: "2026-10-14T10:00:00Z"}, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := RemoveBeingPlanned(dir, "/a/one/"); err != nil {
			t.Fatal(err)
		}
		got := ReadBeingPlanned(dir)
		if len(got) != 1 || got[0].Root != "/a/two" {
			t.Errorf("list = %+v, want only /a/two", got)
		}
	})
	t.Run("unlisted root writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		if err := RemoveBeingPlanned(dir, "/a/one"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(BeingPlannedPath(dir)); !os.IsNotExist(err) {
			t.Errorf("file exists after a no-op remove (stat err %v)", err)
		}
	})
}

// TestBeingPlannedPrune: the "nothing gone" row is load-bearing because the
// page prunes on every start and must not rewrite an unchanged file; the
// "drops gone" row shows the rest are kept.
func TestBeingPlannedPrune(t *testing.T) {
	t.Run("drops gone and keeps the rest", func(t *testing.T) {
		dir := t.TempDir()
		for _, r := range []string{"/a/one", "/a/two", "/a/three"} {
			if _, err := AddBeingPlanned(dir, BeingPlanned{Root: r, At: "2026-10-14T10:00:00Z"}, nil); err != nil {
				t.Fatal(err)
			}
		}
		err := PruneBeingPlanned(dir, func(root string) bool { return root == "/a/two" })
		if err != nil {
			t.Fatal(err)
		}
		got := ReadBeingPlanned(dir)
		if len(got) != 2 || got[0].Name != "one" || got[1].Name != "three" {
			t.Errorf("list = %+v, want one and three", got)
		}
	})
	t.Run("nothing gone writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := AddBeingPlanned(dir, BeingPlanned{Root: "/a/one", At: "2026-10-14T10:00:00Z"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(BeingPlannedPath(dir)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(BeingPlannedPath(dir), []byte(`{"projects":[{"name":"one","root":"/a/one","at":"2026-10-14T10:00:00Z"}]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(BeingPlannedPath(dir))
		if err := PruneBeingPlanned(dir, func(string) bool { return false }); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(BeingPlannedPath(dir))
		if string(before) != string(after) {
			t.Errorf("file rewritten: %q -> %q", before, after)
		}
	})
}

// TestBeingPlannedDamaged: every damaged shape must read as an empty list,
// or the page cannot load; the next add then replaces the file.
func TestBeingPlannedDamaged(t *testing.T) {
	tests := []struct {
		name    string
		content *string
	}{
		{"missing file", nil},
		{"empty file", strPtr("")},
		{"not JSON", strPtr("{projects:")},
		{"wrong shape", strPtr(`{"projects":"yes"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content != nil {
				if err := os.WriteFile(BeingPlannedPath(dir), []byte(*tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := ReadBeingPlanned(dir); len(got) != 0 {
				t.Fatalf("ReadBeingPlanned = %+v, want empty", got)
			}
			if _, err := AddBeingPlanned(dir, BeingPlanned{Root: "/x/proj", At: "2026-10-14T10:00:00Z"}, nil); err != nil {
				t.Fatalf("add after a damaged file: %v", err)
			}
			if got := ReadBeingPlanned(dir); len(got) != 1 {
				t.Errorf("after the next add: %+v", got)
			}
		})
	}
}

// TestBeingPlannedEmptyWritesArray: an emptied list is written as [] and not
// null, so the key is always a list for other readers.
func TestBeingPlannedEmptyWritesArray(t *testing.T) {
	dir := t.TempDir()
	if _, err := AddBeingPlanned(dir, BeingPlanned{Root: "/x/p", At: "2026-10-14T10:00:00Z"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := RemoveBeingPlanned(dir, "/x/p"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(BeingPlannedPath(dir))
	if string(data) != "{\"projects\":[]}\n" {
		t.Errorf("file = %q", data)
	}
}
