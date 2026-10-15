package daemonstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// BeingPlanned is a project whose planning session was started from the web
// page's Plan a new project action and has not been approved yet. Name is the
// folder name the page serves it under, Root is the project's main checkout,
// cleaned, and At is when the start last succeeded, RFC 3339 in UTC. Every key
// is always written.
type BeingPlanned struct {
	Name string `json:"name"`
	Root string `json:"root"`
	At   string `json:"at"`
}

// beingPlannedFileName is the list's name inside the machine state folder.
const beingPlannedFileName = "being-planned.json"

// BeingPlannedPath is being-planned.json inside dir, the same state folder
// that holds plan-reviews.json.
//
// The page shows only projects the daemon lists or the review list names, so a
// project started from the page would vanish (and its chat would be a 404)
// until its plan reached approval. This list keeps it visible in between
// (T8.15).
func BeingPlannedPath(dir string) string {
	return filepath.Join(dir, beingPlannedFileName)
}

// beingPlannedFile is the whole file; the top-level key is always present.
type beingPlannedFile struct {
	Projects []BeingPlanned `json:"projects"`
}

// readBeingPlannedFile returns the list in the file. ok is false when the file
// is missing or damaged; callers then treat the list as empty, because a
// damaged list must not stop the page or a later add.
func readBeingPlannedFile(dir string) ([]BeingPlanned, bool) {
	data, err := os.ReadFile(BeingPlannedPath(dir))
	if err != nil {
		return nil, false
	}
	var f beingPlannedFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, false
	}
	return f.Projects, true
}

// writeBeingPlannedFile replaces the list atomically, as an empty array rather
// than null when there is nothing in it.
func writeBeingPlannedFile(dir string, list []BeingPlanned) error {
	if list == nil {
		list = []BeingPlanned{}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.Marshal(beingPlannedFile{Projects: list})
	if err != nil {
		return err
	}
	if err := writeAtomic(BeingPlannedPath(dir), append(data, '\n')); err != nil {
		return fmt.Errorf("writing being-planned list: %w", err)
	}
	return nil
}

// ReadBeingPlanned returns the projects being planned, sorted by name. A
// missing or damaged file reads as an empty list and never as an error, and
// entries with no root are dropped since nothing can be served for them.
func ReadBeingPlanned(dir string) []BeingPlanned {
	list, _ := readBeingPlannedFile(dir)
	var out []BeingPlanned
	for _, e := range list {
		if e.Root != "" {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AddBeingPlanned lists a project, keyed by its cleaned root, and returns the
// stored entry. A root that is already listed keeps its name and only gets a
// new time: the page's address for the chat must not move when planning is
// resumed. A new root takes e.Name when it is not empty, else its folder name,
// and gets -2, -3 and so on while that name is in taken (names the caller
// knows are used, such as configured projects and review entries) or used by
// another entry, so two projects never share an address.
func AddBeingPlanned(dir string, e BeingPlanned, taken []string) (BeingPlanned, error) {
	if e.Root == "" {
		return BeingPlanned{}, errors.New("a project being planned needs its folder")
	}
	root := filepath.Clean(e.Root)
	t, err := time.Parse(time.RFC3339, e.At)
	if err != nil {
		return BeingPlanned{}, fmt.Errorf("a project being planned needs a time in RFC 3339 form, not %q", e.At)
	}
	at := t.UTC().Format(time.RFC3339)

	list, _ := readBeingPlannedFile(dir)
	for i, have := range list {
		if filepath.Clean(have.Root) == root {
			list[i].Root = root
			list[i].At = at
			if err := writeBeingPlannedFile(dir, list); err != nil {
				return BeingPlanned{}, err
			}
			return list[i], nil
		}
	}
	used := make(map[string]bool, len(list)+len(taken))
	for _, have := range list {
		used[have.Name] = true
	}
	for _, n := range taken {
		used[n] = true
	}
	base := e.Name
	if base == "" {
		base = folderName(root)
	}
	name := base
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	stored := BeingPlanned{Name: name, Root: root, At: at}
	if err := writeBeingPlannedFile(dir, append(list, stored)); err != nil {
		return BeingPlanned{}, err
	}
	return stored, nil
}

// RemoveBeingPlanned takes a project off the list, matching on the cleaned
// root. A root that is not listed is not an error and nothing is written:
// approve calls this on every run.
func RemoveBeingPlanned(dir, root string) error {
	root = filepath.Clean(root)
	list, ok := readBeingPlannedFile(dir)
	if !ok {
		return nil
	}
	kept := list[:0:0]
	removed := false
	for _, e := range list {
		if filepath.Clean(e.Root) == root {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return nil
	}
	return writeBeingPlannedFile(dir, kept)
}

// PruneBeingPlanned removes every entry for which gone reports true, and
// writes only when something was removed. The caller decides what "gone"
// means (the planning session is finished or has no state), because that
// reads a project's folder, which this package does not do.
func PruneBeingPlanned(dir string, gone func(root string) bool) error {
	list, ok := readBeingPlannedFile(dir)
	if !ok {
		return nil
	}
	kept := list[:0:0]
	removed := false
	for _, e := range list {
		if gone(e.Root) {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return nil
	}
	return writeBeingPlannedFile(dir, kept)
}
