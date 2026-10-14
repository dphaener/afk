// Package planfolder decides what a line typed into the dashboard's new-plan
// form means and makes the folder when it names a new project (T7.5).
//
// The form has always taken a folder path. A bare name such as "demo" now
// becomes a folder under the owner profile's "project location" entry, so a
// hobbyist does not have to type a full path for a project that does not exist
// yet. Everything here is pure except Create: the profile and the disk are read
// by the caller and passed in, so the whole matrix is table-testable without
// touching a real folder. Create takes its two side effects as functions for
// the same reason.
package planfolder

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dphaener/afk/internal/profile"
)

// locationTopic is the profile topic that names where new projects live.
const locationTopic = "project location"

// setHint is the command that fixes a missing or bad location. It appears in
// several sentences, so it is written once.
const setHint = `afk profile set "project location" ~/code`

// maxName is the longest folder name accepted.
const maxName = 100

// namePattern limits a name to characters that cannot be a path, a hidden
// folder or something a command line would read as a flag.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Location is the profile's project location, ready to use or explained.
type Location struct {
	// Path is the absolute folder, with "~" already expanded. Empty unless Set.
	Path string
	// Set is true when the profile names a location.
	Set bool
	// Problem is a plain sentence when the profile could not be read or the
	// value is unusable; the decision shows it as the reason for a refusal.
	Problem string
}

// LocationFrom finds the general "project location" entry. A kind-specific
// entry is ignored because the form does not know the project's kind yet.
// found is false when the owner has no profile at all, which simply means no
// location is set. home expands "~"; it is a parameter so tests need no real
// home folder.
func LocationFrom(p profile.Profile, found bool, loadErr error, home string) Location {
	if loadErr != nil {
		return Location{Problem: fmt.Sprintf("Your profile could not be read (%v). Run `afk profile check` to see what is wrong.", loadErr)}
	}
	if !found {
		return Location{}
	}
	var value string
	for _, e := range profile.Select(p, "") {
		if profile.TopicKey(e.Topic) == locationTopic {
			value = strings.TrimSpace(e.Value)
			break
		}
	}
	if value == "" || strings.EqualFold(value, profile.NoPreference) {
		return Location{}
	}
	expanded := value
	if value == "~" {
		expanded = home
	} else {
		expanded = profile.ExpandHome(value, home)
	}
	if !filepath.IsAbs(expanded) {
		return Location{Set: true, Problem: fmt.Sprintf("Your project location %q is not a full path. Fix it with `%s`.", value, setHint)}
	}
	return Location{Path: expanded, Set: true}
}

// Kind is what the dashboard should do with the typed text.
type Kind int

const (
	// KindRefuse is the zero value so a Decision that was never filled in can
	// never be mistaken for permission to create something.
	KindRefuse Kind = iota
	// KindUse plans in the typed folder as it is.
	KindUse
	// KindCreate makes the folder first. The Kind names carry a prefix because
	// Create is also the create step's function in this package.
	KindCreate
)

// Decision is the outcome of Decide.
type Decision struct {
	Kind Kind
	// Path is the folder to use or create; for a refusal with Exists it is
	// the folder that is already there.
	Path string
	// Reason is the plain sentence for a refusal.
	Reason string
	// Exists is true when the name's folder is already there, so the dashboard
	// can offer to plan in it.
	Exists bool
}

// IsName reports whether text, trimmed, is a bare name rather than a path.
func IsName(text string) bool {
	t := strings.TrimSpace(text)
	return !strings.ContainsAny(t, "/~")
}

func refuse(format string, args ...any) Decision {
	return Decision{Reason: fmt.Sprintf(format, args...) + " Nothing was created."}
}

// Decide maps the typed text, the profile's location and the disk to one
// outcome. stat is os.Stat in production. Text with a "/" or "~" is a path and
// is passed through unchanged: the dashboard's existing start expands "~/" and
// refuses missing or relative folders in its own words, and repeating those
// checks here would let the two disagree.
func Decide(text string, loc Location, stat func(string) (fs.FileInfo, error)) Decision {
	name := strings.TrimSpace(text)
	if name == "" {
		return refuse("Type a folder, or a name for a new project.")
	}
	if !IsName(name) {
		return Decision{Kind: KindUse, Path: text}
	}
	if len(name) > maxName || !namePattern.MatchString(name) {
		return refuse("%q is not a usable project name. Use letters, digits, `.`, `_` and `-`, starting with a letter or digit, up to %d characters.", name, maxName)
	}
	if loc.Problem != "" {
		return refuse("%s", loc.Problem)
	}
	if !loc.Set {
		return refuse("No project location is set. Set one with `%s`, or type a full path.", setHint)
	}
	info, err := stat(loc.Path)
	switch {
	case err == nil && info.IsDir():
	case err == nil:
		return refuse("Your project location %s is not a folder. Fix it with `%s`, or type a full path.", loc.Path, setHint)
	case errors.Is(err, fs.ErrNotExist):
		return refuse("Your project location %s does not exist. Create it first, or type a full path.", loc.Path)
	default:
		return refuse("Your project location %s could not be checked (%v). Create it first, or type a full path.", loc.Path, err)
	}
	target := filepath.Join(loc.Path, name)
	info, err = stat(target)
	switch {
	case err == nil && info.IsDir():
		d := refuse("%s already exists. The dashboard can plan in it.", target)
		d.Path, d.Exists = target, true
		return d
	case err == nil:
		return refuse("%s already exists and is not a folder. Pick another name.", target)
	case errors.Is(err, fs.ErrNotExist):
		return Decision{Kind: KindCreate, Path: target}
	default:
		return refuse("%s could not be checked (%v). Pick another name or type a full path.", target, err)
	}
}

// Create makes the folder with mkdir, then turns it into a git repository with
// gitInit. Production passes os.Mkdir, never MkdirAll, so a missing location is
// an error rather than something afk invents. A failure removes nothing: the
// owner may already have put something in the folder, and the message says what
// is left so they can decide.
func Create(path string, mkdir func(string) error, gitInit func(string) error) error {
	if err := mkdir(path); err != nil {
		return fmt.Errorf("The folder %s could not be made (%v). Nothing was created.", path, err)
	}
	if err := gitInit(path); err != nil {
		return fmt.Errorf("The folder %s was made and is left on disk, but git init failed (%v). Nothing was removed. Type the full path to plan in it, or remove the folder.", path, err)
	}
	return nil
}
