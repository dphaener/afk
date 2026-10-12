// Package inbox stores the items afk parks for its owner: a question only the
// owner can answer, a credential to set, a thing to check by hand, a failure,
// a notice. Each item is one JSON file, {project}/.afk/inbox/{id}.json, in
// the project's main checkout.
//
// # Why agents never write these files directly
//
// The files have several readers that run at the same time as the writers
// (the `afk inbox` command today; a daemon, a web page and the dashboard
// later), and every one of them reads strictly. A file an agent wrote by hand
// with one key misspelt, a missing key or a reused id would not be a slightly
// wrong item: it would be skipped, and the owner would never see the one
// thing the agent stopped to ask. So the only writer is this package, reached
// through the `afk inbox` command, which validates the item, picks an id
// nobody else has, and puts the whole file in place in one rename.
//
// # Why reads are strict, and why an unreadable file is skipped
//
// Read decodes with unknown keys rejected and nothing allowed after the
// object, and also refuses a file whose `id` is not its file name or whose
// `kind` is not one of the six. A lenient reader would show a half-understood
// item as if it were whole, and the answer the owner typed would then be
// written back over whatever the reader did not understand. But one bad file
// must not hide the rest of the inbox, so List reports unreadable files by
// name in a separate return value instead of failing: the caller can say "2
// inbox files could not be read" and still show everything else.
//
// # Why only {id}.json names count
//
// The folder sits under `.afk/`, beside the session folders, where agents and
// tools drop notes, logs and status files freely. Anything not named exactly
// {six characters from the id alphabet}.json is ignored without comment: the
// lock, a temp file left by a killed process, a stray notes.json, a
// subfolder. That keeps "unreadable" meaning "this was meant to be an item
// and is broken". (A stray status.json is the one surprise: "status" happens
// to be six characters from the id alphabet, so it is read as an item, fails
// the strict decode, and is reported as unreadable rather than ignored.)
//
// # Why the lock exists
//
// Two steps here are check-then-act and would race without it. Add looks for
// a free id and then writes that file; two adds at once could pick the same
// id and one item would silently replace the other. Answer reads an item and
// writes it back; the first answer is the one that is kept, and two answers
// arriving together (the command line and the web page, say) could otherwise
// both believe they were first. An exclusive flock on {Dir}/.lock makes each
// of those one step. List and Get take no lock: every write is a rename of a
// complete file, so a reader sees the old item or the new one, never part of
// one.
//
// The package imports only the standard library. It makes no network calls
// and reads no configuration; who gets notified about a new item is the
// caller's business.
package inbox

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// idAlphabet leaves out the characters that are easy to confuse when an id is
// read off a phone notification and typed into a terminal: i, l, o, 0 and 1.
const idAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

const (
	idLength = 6

	// maxIDTries bounds the search for a free id. With 31^6 possible ids a
	// second try is already rare; twenty misses in a row means the generator
	// is broken (a scripted one in a test, or a constant), and looping
	// forever while holding the lock would hang every other writer.
	maxIDTries = 20

	// maxTitleChars keeps a title to what fits in a notification's title and
	// in one line of the list.
	maxTitleChars = 200

	lockName = ".lock"
)

// kinds is the closed set of item kinds. It is a slice, not a map, so the
// validation message can list them in a fixed order.
var kinds = []string{"question", "env_var", "external_action", "verification", "failure", "notice"}

// The places an answer can come from, as stored in `answered_via`. They are
// exported so each caller of Answer (the command, the web page, the
// dashboard) names its own place instead of retyping the word.
const (
	ViaWeb = "web"
	ViaCLI = "cli"
	ViaTUI = "tui"
)

// vias is the closed set of places an answer can come from.
var vias = []string{ViaWeb, ViaCLI, ViaTUI}

var (
	// taskRe matches a master-plan task id such as T1.2 or T4.2a.
	taskRe = regexp.MustCompile(`^T[0-9]+\.[0-9]+[a-z]?$`)

	// sessionRe is the session-name shape. It is written out here rather
	// than taken from internal/session because importing that package would
	// pull tool probing and desktop notifications into every reader of the
	// inbox.
	sessionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

var (
	// ErrNotFound means there is no item with that id. An id that is not
	// shaped like an id at all gets the same error: to the person who typed
	// it, both are "no such item".
	ErrNotFound = errors.New("inbox item not found")

	// ErrAlreadyAnswered means the item has an answer and it was kept.
	// Answer returns the stored item alongside it so the caller can show
	// the answer that stands.
	ErrAlreadyAnswered = errors.New("inbox item already answered")

	// ErrEmptyAnswer means the answer was empty once trimmed.
	ErrEmptyAnswer = errors.New("inbox answer is empty")

	// errNoDir guards against a zero Store. An empty Dir would otherwise
	// resolve to the working directory and scatter a lock and item files
	// into whatever folder the process happened to start in.
	errNoDir = errors.New("inbox store has no directory")
)

// ValidationError says what is wrong with a new item, as one plain sentence
// with no prefix, so a command can print it after its own name.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// Item is one inbox file. All fourteen keys are always written: a reader in
// another language (the web page) can rely on every key being present and on
// `options` being an array.
type Item struct {
	ID          string   `json:"id"`
	CreatedAt   string   `json:"created_at"`
	Task        string   `json:"task"`
	Session     string   `json:"session"`
	Kind        string   `json:"kind"`
	Blocking    bool     `json:"blocking"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Options     []string `json:"options"`
	Recommended string   `json:"recommended"`
	Assumed     string   `json:"assumed"`
	Answer      string   `json:"answer"`
	AnsweredAt  string   `json:"answered_at"`
	AnsweredVia string   `json:"answered_via"`
}

// NewItem is what a caller supplies; the store fills in the id, the creation
// time and the (empty) answer fields.
type NewItem struct {
	Task, Session, Kind string
	Blocking            bool
	Title, Body         string
	Options             []string
	Recommended         string
	Assumed             string
}

// Open reports whether the item still has no answer.
func (it Item) Open() bool { return it.Answer == "" }

// Dir returns the inbox folder of a project. project must be the main
// checkout, not a session worktree: the inbox is shared by every session.
func Dir(project string) string {
	return filepath.Join(project, ".afk", "inbox")
}

// ValidID reports whether s is shaped like an item id. It is also the gate
// that keeps a caller-supplied id from being used as a path: no separator,
// dot or parent reference is in the alphabet.
func ValidID(s string) bool {
	if len(s) != idLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(idAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// Validate checks a new item and returns a *ValidationError for the first
// rule it breaks. It is pure: Add calls it before touching the disk, so a
// refused item leaves nothing behind, not even the folder.
func Validate(n NewItem) error {
	if msg := validate(n); msg != "" {
		return &ValidationError{Message: msg}
	}
	return nil
}

func validate(n NewItem) string {
	kindList := strings.Join(kinds[:len(kinds)-1], ", ") + " or " + kinds[len(kinds)-1]
	if n.Kind == "" {
		return "the item needs a kind; use one of " + kindList + "."
	}
	if !slices.Contains(kinds, n.Kind) {
		return fmt.Sprintf("%q is not a kind of inbox item; use one of %s.", n.Kind, kindList)
	}

	title := strings.TrimSpace(n.Title)
	if title == "" {
		return "the item needs a title; write one line saying what the owner has to do or know."
	}
	if strings.ContainsAny(title, "\r\n") {
		return "the title must be a single line; put the rest in the body."
	}
	if c := utf8.RuneCountInString(title); c > maxTitleChars {
		return fmt.Sprintf("the title is %d characters long and the limit is %d; shorten it and put the detail in the body.", c, maxTitleChars)
	}

	if n.Kind != "question" {
		if len(n.Options) > 0 {
			return fmt.Sprintf("options only belong on a question, and this item is %s; remove the options or make the kind question.", withArticle(n.Kind))
		}
		if n.Recommended != "" {
			return fmt.Sprintf("a recommended option only belongs on a question, and this item is %s; remove it or make the kind question.", withArticle(n.Kind))
		}
	}
	if len(n.Options) == 1 {
		return "a question with options needs at least two of them; add another option or remove the one given."
	}
	seen := make(map[string]bool, len(n.Options))
	for i, o := range n.Options {
		if strings.TrimSpace(o) == "" {
			return fmt.Sprintf("option %d is empty; give every option some text.", i+1)
		}
		if seen[o] {
			return fmt.Sprintf("the option %q is given twice; list each option once.", o)
		}
		seen[o] = true
	}
	if n.Recommended != "" && !seen[n.Recommended] {
		if len(n.Options) == 0 {
			return fmt.Sprintf("the recommended option %q has no options to choose from; list the options, including that one.", n.Recommended)
		}
		return fmt.Sprintf("the recommended option %q is not one of the options; write it exactly as one of them.", n.Recommended)
	}

	if n.Assumed != "" && n.Blocking {
		return "an assumed answer means the task carried on, but this item stops the task; remove the assumed answer or make the item non-blocking."
	}

	if n.Task != "" && !taskRe.MatchString(n.Task) {
		return fmt.Sprintf("%q is not a task id; write it like T4.3 or T4.2a, or leave the task out.", n.Task)
	}
	if n.Session != "" && !sessionRe.MatchString(n.Session) {
		return fmt.Sprintf("%q is not a session name; use lowercase letters, digits and hyphens, starting with a letter or digit, or leave the session out.", n.Session)
	}
	return ""
}

// withArticle is only for the validation messages: "an env_var", "a notice".
func withArticle(kind string) string {
	if strings.ContainsRune("aeiou", rune(kind[0])) {
		return "an " + kind
	}
	return "a " + kind
}

// ResolveAnswer turns what the owner typed into the answer to store. On an
// item with options a whole number in range picks that option, because the
// options are shown numbered and a phone keyboard makes "2" much easier than
// retyping the text. Anything else, including a number out of range, is kept
// as typed: the owner may be answering with something that is not on the
// list.
func ResolveAnswer(it Item, raw string) string {
	text := strings.TrimSpace(raw)
	if len(it.Options) > 0 && isDigits(text) {
		if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(it.Options) {
			return it.Options[n-1]
		}
	}
	return text
}

// isDigits is stricter than strconv.Atoi, which also accepts a sign: "+2" is
// not what a numbered list shows, so it is kept as text.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Store reads and writes the items of one inbox folder. The zero values of
// Now and NewID mean the real clock and the random id generator; tests set
// both.
type Store struct {
	Dir   string
	Now   func() time.Time
	NewID func() string
}

func (s Store) now() string {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return now().UTC().Format(time.RFC3339)
}

func (s Store) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	return randomID()
}

func (s Store) path(id string) string {
	return filepath.Join(s.Dir, id+".json")
}

// randomID draws six characters from idAlphabet. A byte of 248 or more is
// thrown away rather than reduced: 248 is the largest multiple of 31 that
// fits in a byte, so keeping the tail would make the first eight characters
// of the alphabet slightly more likely than the rest.
func randomID() string {
	const limit = 256 - 256%len(idAlphabet)
	out := make([]byte, 0, idLength)
	buf := make([]byte, idLength*2)
	for len(out) < idLength {
		// crypto/rand.Read is documented never to return an error; it
		// stops the program instead if the system has no randomness.
		_, _ = rand.Read(buf)
		for _, b := range buf {
			if int(b) < limit && len(out) < idLength {
				out = append(out, idAlphabet[int(b)%len(idAlphabet)])
			}
		}
	}
	return string(out)
}

// Add validates the item, gives it an id and a creation time, and writes its
// file. Nothing is created when validation fails.
func (s Store) Add(n NewItem) (Item, error) {
	if err := Validate(n); err != nil {
		return Item{}, err
	}
	if s.Dir == "" {
		return Item{}, errNoDir
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Item{}, fmt.Errorf("creating the inbox folder %s: %w", s.Dir, err)
	}
	unlock, err := s.lock()
	if err != nil {
		return Item{}, err
	}
	defer unlock()

	id, err := s.freeID()
	if err != nil {
		return Item{}, err
	}

	// Never nil, and never the caller's slice: a nil slice would be written
	// as `null`, and the item returned here must not change if the caller
	// reuses its own slice.
	options := make([]string, len(n.Options))
	copy(options, n.Options)

	it := Item{
		ID:          id,
		CreatedAt:   s.now(),
		Task:        n.Task,
		Session:     n.Session,
		Kind:        n.Kind,
		Blocking:    n.Blocking,
		Title:       strings.TrimSpace(n.Title),
		Body:        strings.TrimSpace(n.Body),
		Options:     options,
		Recommended: n.Recommended,
		Assumed:     n.Assumed,
	}
	if err := s.write(it); err != nil {
		return Item{}, err
	}
	return it, nil
}

// freeID must be called with the lock held: the check and the write that
// follows are only one step because nobody else can add in between.
func (s Store) freeID() (string, error) {
	for range maxIDTries {
		id := s.newID()
		if !ValidID(id) {
			// Writing it would create a file List ignores: an item that
			// was "added" and can never be seen.
			return "", fmt.Errorf("the id generator returned %q, which is not a valid inbox id", id)
		}
		_, err := os.Lstat(s.path(id))
		if os.IsNotExist(err) {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("checking for inbox item %s: %w", id, err)
		}
	}
	return "", fmt.Errorf("could not find a free inbox id in %d tries", maxIDTries)
}

// List returns every readable item, the ones that need the owner first: open
// and blocking, then open, then answered; oldest first inside each group, and
// by id when two were created in the same second. The names of files that
// look like items but cannot be read come back in unreadable, sorted. A
// folder that does not exist yet is an empty inbox, not an error: nothing has
// been parked.
func (s Store) List() (items []Item, unreadable []string, err error) {
	if s.Dir == "" {
		return nil, nil, errNoDir
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading the inbox folder %s: %w", s.Dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		id, ok := strings.CutSuffix(name, ".json")
		if !ok || !ValidID(id) {
			continue
		}
		it, err := readItem(s.path(id), id)
		if err != nil {
			unreadable = append(unreadable, name)
			continue
		}
		items = append(items, it)
	}
	slices.SortFunc(items, compareItems)
	slices.Sort(unreadable)
	return items, unreadable, nil
}

func compareItems(a, b Item) int {
	if ra, rb := rank(a), rank(b); ra != rb {
		return ra - rb
	}
	// Compared as times, not strings: this package always writes UTC, but a
	// file written elsewhere with a zone offset is still a valid RFC 3339
	// time and would sort wrongly as text. A time that does not parse is the
	// zero time, so it sorts first rather than at random.
	ta, _ := time.Parse(time.RFC3339, a.CreatedAt)
	tb, _ := time.Parse(time.RFC3339, b.CreatedAt)
	if c := ta.Compare(tb); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

func rank(it Item) int {
	switch {
	case !it.Open():
		return 2
	case it.Blocking:
		return 0
	default:
		return 1
	}
}

// Get returns one item. ErrNotFound covers both a missing file and an id that
// is not shaped like one; a file that is there but cannot be read strictly is
// a different error, wrapping the reason.
func (s Store) Get(id string) (Item, error) {
	if s.Dir == "" {
		return Item{}, errNoDir
	}
	if !ValidID(id) {
		return Item{}, ErrNotFound
	}
	it, err := readItem(s.path(id), id)
	if err != nil {
		if os.IsNotExist(err) {
			return Item{}, ErrNotFound
		}
		return Item{}, fmt.Errorf("inbox item %s could not be read: %w", id, err)
	}
	return it, nil
}

// Answer records the owner's answer. The first answer is kept: a second call
// returns the stored item with ErrAlreadyAnswered and changes nothing, so an
// agent that has already acted on an answer never finds it replaced. via says
// where the answer came from and must be web, cli or tui.
func (s Store) Answer(id, text, via string) (Item, error) {
	if s.Dir == "" {
		return Item{}, errNoDir
	}
	if !slices.Contains(vias, via) {
		return Item{}, fmt.Errorf("%q is not a place an inbox answer can come from; it must be %s", via, strings.Join(vias, ", "))
	}
	if !ValidID(id) {
		return Item{}, ErrNotFound
	}
	path := s.path(id)
	// Checked before the lock is opened, so a mistyped id (or a project
	// with no inbox at all) does not leave a lock file behind.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Item{}, ErrNotFound
		}
		return Item{}, fmt.Errorf("checking inbox item %s: %w", id, err)
	}

	unlock, err := s.lock()
	if err != nil {
		return Item{}, err
	}
	defer unlock()

	// Read again under the lock: the answer that decides "already answered"
	// must be the one on disk now, not the one seen before waiting.
	it, err := readItem(path, id)
	if err != nil {
		if os.IsNotExist(err) {
			return Item{}, ErrNotFound
		}
		return Item{}, fmt.Errorf("inbox item %s could not be read: %w", id, err)
	}
	if !it.Open() {
		return it, ErrAlreadyAnswered
	}
	answer := ResolveAnswer(it, text)
	if answer == "" {
		return it, ErrEmptyAnswer
	}
	it.Answer = answer
	it.AnsweredAt = s.now()
	it.AnsweredVia = via
	if err := s.write(it); err != nil {
		return Item{}, err
	}
	return it, nil
}

// lock takes the exclusive lock on {Dir}/.lock and returns the function that
// releases it. The lock is a separate file, never an item file, because item
// files are replaced by rename and a lock held on a replaced file guards
// nothing. flock blocks until the holder lets go; holders only ever do a
// handful of small local file operations.
func (s Store) lock() (unlock func(), err error) {
	lp := filepath.Join(s.Dir, lockName)
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening the inbox lock %s: %w", lp, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", lp, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// write puts the item's file in place atomically. Must be called with the
// lock held.
func (s Store) write(it Item) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Titles and bodies are prose and often hold `&`, `<` or `>`; left on,
	// the escaping turns them into & and friends, which is still valid
	// JSON but unreadable to someone who opens the file.
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// Encode ends the output with the trailing newline.
	if err := enc.Encode(it); err != nil {
		return fmt.Errorf("encoding inbox item %s: %w", it.ID, err)
	}

	// The temp file must be in the inbox folder: rename is only atomic
	// within one filesystem. Its leading dot keeps it out of List. The pid
	// in the name is enough to keep writers apart because the lock already
	// allows one at a time; it is there so a leftover from a killed process
	// is recognisable and gets overwritten.
	tmp := filepath.Join(s.Dir, fmt.Sprintf(".%s.json.tmp.%d", it.ID, os.Getpid()))
	if err := writeFileSync(tmp, buf.Bytes(), 0o644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing inbox item %s: %w", it.ID, err)
	}
	if err := os.Rename(tmp, s.path(it.ID)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("putting inbox item %s in place: %w", it.ID, err)
	}
	return nil
}

// writeFileSync is os.WriteFile plus an fsync before close. Without the sync
// a power loss shortly after the rename can leave the item's name pointing at
// a file whose contents never reached the disk: an empty, unreadable item in
// place of the thing the owner was asked.
func writeFileSync(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readItem reads one file strictly. A missing file comes back as the
// os.ReadFile error, so callers can tell it apart with os.IsNotExist.
func readItem(path, id string) (Item, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Item{}, err
	}
	var it Item
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&it); err != nil {
		return Item{}, err
	}
	// The file is one JSON object and nothing else. A second Decode that
	// does not hit the end of the input found something after the object.
	// (json.Decoder.More is not used for this: it answers "is there another
	// element", and reports false for a stray closing bracket.)
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return Item{}, errors.New("trailing data after the JSON object")
	}
	if it.ID != id {
		return Item{}, fmt.Errorf("the file is named %s.json but its id is %q", id, it.ID)
	}
	if !slices.Contains(kinds, it.Kind) {
		return Item{}, fmt.Errorf("unknown kind %q", it.Kind)
	}
	if it.Options == nil {
		// `"options": null` decodes to nil. Callers are promised an array.
		it.Options = []string{}
	}
	return it, nil
}
