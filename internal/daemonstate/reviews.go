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

// Review is one plan waiting for the owner's approval. Name is the folder
// name the web page serves the plan under, and Root is the project's main
// checkout, cleaned. At is when the plan was last put up for review, RFC 3339
// in UTC. Every key is always written, so a reader never has to tell absent
// from empty.
type Review struct {
	Name string `json:"name"`
	Root string `json:"root"`
	At   string `json:"at"`
}

// reviewsFileName is the review list's name inside the machine state folder.
const reviewsFileName = "plan-reviews.json"

// ReviewsPath is plan-reviews.json inside dir. dir is afk's per-machine state
// folder, the one usagestate.Dir returns and governor.json lives in.
//
// The list is written by `afk plan advance` (when a new plan reaches review)
// and `afk plan approve` (which removes the entry), and read by the web page.
// It is not in .afk/plan/ because the page has to find a project the machine
// config does not list yet: a plan waiting for approval is, by definition,
// not in config.toml. A project's own folder is no place to look either,
// since the page would then need to know every project path before it could
// show one.
func ReviewsPath(dir string) string {
	return filepath.Join(dir, reviewsFileName)
}

// reviewFile is the whole file. The top-level key is always present, even
// when the list is empty.
type reviewFile struct {
	Reviews []Review `json:"reviews"`
}

// readReviewFile returns the list in the file. ok is false when the file is
// missing or damaged; callers then treat the list as empty, because a damaged
// list must not stop the page or a later add.
func readReviewFile(dir string) ([]Review, bool) {
	data, err := os.ReadFile(ReviewsPath(dir))
	if err != nil {
		return nil, false
	}
	var f reviewFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, false
	}
	return f.Reviews, true
}

// writeReviewFile replaces the list atomically. The list is written as an
// empty array rather than null, so the key is always a list.
func writeReviewFile(dir string, reviews []Review) error {
	if reviews == nil {
		reviews = []Review{}
	}
	sort.SliceStable(reviews, func(i, j int) bool { return reviews[i].Name < reviews[j].Name })
	data, err := json.Marshal(reviewFile{Reviews: reviews})
	if err != nil {
		return err
	}
	if err := writeAtomic(ReviewsPath(dir), append(data, '\n')); err != nil {
		return fmt.Errorf("writing plan review list: %w", err)
	}
	return nil
}

// ReadReviews returns the plans waiting for approval, sorted by name. A
// missing or damaged file reads as an empty list and never as an error: the
// page must still load when the file is broken, and the next add replaces it.
// Entries with no root are dropped, since nothing can be served for them.
func ReadReviews(dir string) []Review {
	reviews, _ := readReviewFile(dir)
	var out []Review
	for _, r := range reviews {
		if r.Root != "" {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AddReview puts a plan on the list, keyed by its cleaned root. The caller
// supplies Root and At; Name in r is ignored because this function chooses it.
//
// Adding a root that is already listed replaces its time and keeps its name:
// the page's address for the plan must not move when the plan is put up for
// review again. A new root is named after its folder, and when another root
// already has that name it gets -2, -3 and so on, so two folders with the
// same name both get a page instead of one hiding the other.
func AddReview(dir string, r Review) error {
	if r.Root == "" {
		return errors.New("a plan review needs the project folder")
	}
	root := filepath.Clean(r.Root)
	at, err := time.Parse(time.RFC3339, r.At)
	if err != nil {
		return fmt.Errorf("a plan review needs a time in RFC 3339 form, not %q", r.At)
	}
	at = at.UTC()

	reviews, _ := readReviewFile(dir)
	for i, e := range reviews {
		if filepath.Clean(e.Root) == root {
			reviews[i].Root = root
			reviews[i].At = at.Format(time.RFC3339)
			return writeReviewFile(dir, reviews)
		}
	}
	taken := make(map[string]bool, len(reviews))
	for _, e := range reviews {
		taken[e.Name] = true
	}
	name := folderName(root)
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d", folderName(root), n)
	}
	reviews = append(reviews, Review{Name: name, Root: root, At: at.Format(time.RFC3339)})
	return writeReviewFile(dir, reviews)
}

// RemoveReview takes a plan off the list, matching on the cleaned root. A
// root that is not listed is not an error, and nothing is written in that
// case: approve calls this on every run, including the ones after the first.
func RemoveReview(dir, root string) error {
	root = filepath.Clean(root)
	reviews, ok := readReviewFile(dir)
	if !ok {
		return nil
	}
	kept := reviews[:0:0]
	removed := false
	for _, e := range reviews {
		if filepath.Clean(e.Root) == root {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return nil
	}
	return writeReviewFile(dir, kept)
}

// folderName is the last element of a cleaned root. A root with no usable
// last element ("/" or ".") still needs a name for the page, so it gets a
// fixed one.
func folderName(root string) string {
	base := filepath.Base(root)
	if base == "/" || base == "." || base == "" {
		return "project"
	}
	return base
}
