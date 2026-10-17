package daemonconfig

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// errUnsafeEdit is what the edits return when they cannot prove their own
// result. The caller still has the original text, so nothing is lost.
var errUnsafeEdit = errors.New("afk could not edit config.toml safely, so nothing was changed; change the [[project]] entries by hand in an editor")

// entry is one [[project]] as written, after trimming and cleaning but with a
// leading ~ kept: the edits compare what the owner wrote, because they take
// no home directory and must not need one.
type entry struct{ name, path string }

// effName is the name Load would give the project: the written name, or the
// folder name.
func (e entry) effName() string {
	if e.name != "" {
		return e.name
	}
	return filepath.Base(e.path)
}

// cleanPath applies Load's path rules without expanding ~: absolute or
// starting with ~/ (or just ~), then cleaned. The result keeps ~ as written.
func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("the project path is empty; give an absolute path")
	}
	if p != "~" && !strings.HasPrefix(p, "~/") && !filepath.IsAbs(p) {
		return "", fmt.Errorf("the project path %q is relative; write an absolute path or start it with ~/", p)
	}
	return filepath.Clean(p), nil
}

// parseEntries decodes text and returns its project entries plus the rest of
// the file with the projects blanked, so two texts can be compared for "same
// everything except the projects".
func parseEntries(text string) ([]entry, rawFile, error) {
	var raw rawFile
	if _, err := toml.Decode(text, &raw); err != nil {
		return nil, raw, err
	}
	var out []entry
	for _, p := range raw.Project {
		path := strings.TrimSpace(p.Path)
		if path != "" {
			path = filepath.Clean(path)
		}
		out = append(out, entry{name: strings.TrimSpace(p.Name), path: path})
	}
	raw.Project = nil
	return out, raw, nil
}

// sameEntries compares two lists, treating nil and empty as equal.
func sameEntries(a, b []entry) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}

func quote(s string) string { return strconv.Quote(s) }

func lineEnding(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	}
	return ""
}

func isHeader(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "[") }

func isProjectHeader(line string) bool {
	t := strings.TrimSpace(line)
	if i := strings.Index(t, "#"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t == "[[project]]"
}

// AddProject appends one [[project]] block to config text. It is pure: text
// in, text out. A project whose cleaned path is already listed gives
// added == false and the text unchanged; a name used by a different path is
// an error that names it, since Load would refuse the file. The name is
// written only when it differs from the folder name.
//
// The edit is made on the text so comments and every other table survive. The
// result is parsed again and must be the old file plus exactly this one
// project; if not, the original text comes back with an error. Paths are
// compared as written (a leading ~ is not expanded), so "~/x" and the same
// folder spelled out are two entries here, as in the file.
func AddProject(text string, p Project) (newText string, added bool, err error) {
	path, perr := cleanPath(p.Path)
	if perr != nil {
		return text, false, perr
	}
	e := entry{name: strings.TrimSpace(p.Name), path: path}
	if e.name == filepath.Base(path) {
		e.name = ""
	}
	old, oldRest, derr := parseEntries(text)
	if derr != nil {
		return text, false, fmt.Errorf("config.toml has a mistake in it, fix it first: %w", derr)
	}
	for _, o := range old {
		if o.path == path {
			return text, false, nil
		}
	}
	for _, o := range old {
		if o.effName() == e.effName() {
			return text, false, fmt.Errorf("the name %q is already used by the project at %s; give this one a different name", e.effName(), o.path)
		}
	}

	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.SplitAfter(text, "\n")
	n := len(lines)
	for n > 0 && strings.TrimSpace(lines[n-1]) == "" {
		n--
	}
	var b strings.Builder
	if n > 0 {
		for _, l := range lines[:n] {
			b.WriteString(l)
		}
		if lineEnding(lines[n-1]) == "" {
			b.WriteString(nl)
		}
		b.WriteString(nl)
	}
	b.WriteString("[[project]]" + nl)
	b.WriteString("path = " + quote(path) + nl)
	if e.name != "" {
		b.WriteString("name = " + quote(e.name) + nl)
	}
	newText = b.String()

	want := append(append([]entry{}, old...), e)
	got, gotRest, gerr := parseEntries(newText)
	if gerr != nil || !sameEntries(got, want) || !reflect.DeepEqual(gotRest, oldRest) {
		return text, false, errUnsafeEdit
	}
	return newText, true, nil
}

// RemoveProject deletes the one [[project]] block whose name or cleaned path
// is nameOrPath. No match gives removed == false and the text unchanged.
//
// The block runs from its header to its last line that is neither blank nor a
// comment, so comments that introduce the next table stay. The result is
// parsed again and must be the old file minus that one project, with every
// other table equal; otherwise the original text and an error come back.
func RemoveProject(text string, nameOrPath string) (newText string, removed bool, err error) {
	key := strings.TrimSpace(nameOrPath)
	if key == "" {
		return text, false, errors.New("give the name or the path of the project to remove")
	}
	cleaned := filepath.Clean(key)
	old, oldRest, derr := parseEntries(text)
	if derr != nil {
		return text, false, fmt.Errorf("config.toml has a mistake in it, fix it first: %w", derr)
	}
	idx := -1
	for i, o := range old {
		if o.effName() == key || o.path == cleaned {
			if idx >= 0 {
				return text, false, fmt.Errorf("%q matches more than one project; use the full path", nameOrPath)
			}
			idx = i
		}
	}
	if idx < 0 {
		return text, false, nil
	}

	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Find the idx-th [[project]] header by line.
	start, seen := -1, 0
	for i, l := range lines {
		if isProjectHeader(l) {
			if seen == idx {
				start = i
				break
			}
			seen++
		}
	}
	if start < 0 {
		return text, false, errUnsafeEdit
	}
	end := start
	for i := start + 1; i < len(lines); i++ {
		if isHeader(lines[i]) {
			break
		}
		t := strings.TrimSpace(lines[i])
		if t != "" && !strings.HasPrefix(t, "#") {
			end = i
		}
	}
	// Drop one blank line before the block when one would otherwise double up
	// with the blank after it (or dangle at the end of the file).
	from := start
	afterBlank := end+1 >= len(lines) || strings.TrimSpace(lines[end+1]) == ""
	if from > 0 && strings.TrimSpace(lines[from-1]) == "" && afterBlank {
		from--
	}
	after := end + 1
	if from == 0 && after < len(lines) && strings.TrimSpace(lines[after]) == "" {
		after++
	}
	var b strings.Builder
	for _, l := range lines[:from] {
		b.WriteString(l)
	}
	for _, l := range lines[after:] {
		b.WriteString(l)
	}
	newText = b.String()

	want := append(append([]entry{}, old[:idx]...), old[idx+1:]...)
	got, gotRest, gerr := parseEntries(newText)
	if gerr != nil || !sameEntries(got, want) || !reflect.DeepEqual(gotRest, oldRest) {
		return text, false, errUnsafeEdit
	}
	return newText, true, nil
}

// errUnsafeWebEdit is what SetWeb returns when it cannot prove its own
// result. The caller still has the original text, so nothing is lost.
var errUnsafeWebEdit = errors.New("afk could not edit the [web] part of config.toml safely, so nothing was changed; set bind or public_url under [web] by hand in an editor")

// isWebHeader reports whether line is the [web] table header, ignoring spaces
// and a trailing comment.
func isWebHeader(line string) bool {
	t := strings.TrimSpace(line)
	if i := strings.Index(t, "#"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t == "[web]"
}

// assignmentRest returns, for a line of the form `key = value ...`, the byte
// offsets where the value starts and where it ends (the trailing spaces and
// comment, if any, begin there). ok is false when the line does not assign
// key. Only a single-line basic or literal string, or a bare value, is
// understood; anything stranger fails the re-parse check in SetWeb rather
// than being guessed at here.
func assignmentRest(line, key string) (start, end int, ok bool) {
	t := strings.TrimLeft(line, " \t")
	lead := len(line) - len(t)
	if !strings.HasPrefix(t, key) {
		return 0, 0, false
	}
	i := lead + len(key)
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= len(line) || line[i] != '=' {
		return 0, 0, false
	}
	i++
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	start = i
	switch {
	case i < len(line) && line[i] == '"':
		for i++; i < len(line) && line[i] != '"'; i++ {
			if line[i] == '\\' {
				i++
			}
		}
		if i < len(line) {
			i++
		}
	case i < len(line) && line[i] == '\'':
		for i++; i < len(line) && line[i] != '\''; i++ {
		}
		if i < len(line) {
			i++
		}
	default:
		for i < len(line) && line[i] != '#' && line[i] != '\r' && line[i] != '\n' {
			i++
		}
		for i > start && (line[i-1] == ' ' || line[i-1] == '\t') {
			i--
		}
	}
	if i > len(line) {
		i = len(line)
	}
	return start, i, true
}

// SetWeb sets bind or public_url inside the [web] table of config text. It
// is pure: text in, text out, and it never writes a file. Any other key is an
// error, and so is token: a secret must never be written to config.toml.
//
// The value is checked with the rules Load applies to [web], so an edit never
// produces a file Load would refuse. An existing assignment of the key is
// replaced in place, keeping its trailing comment and line ending; otherwise
// the line goes at the end of the [web] table (before the comments that
// introduce the next table), and with no [web] table a new one is appended.
//
// As with AddProject the result is parsed again and must be the old file with
// only that key changed, holding the new value; if not, the original text
// comes back with an error about [web].
func SetWeb(text, key, value string) (newText string, err error) {
	if key != "bind" && key != "public_url" {
		return text, fmt.Errorf("afk only sets bind and public_url under [web], not %q", key)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return text, fmt.Errorf("the [web] %s is empty; give a value", key)
	}
	check := rawFile{}
	if key == "bind" {
		check.Web.Bind = value
	} else {
		check.Web.PublicURL = value
	}
	if _, verr := loadWeb("config.toml", check); verr != nil {
		return text, verr
	}
	var oldRaw rawFile
	if _, derr := toml.Decode(text, &oldRaw); derr != nil {
		return text, fmt.Errorf("config.toml has a mistake in it, fix it first: %w", derr)
	}

	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	newLine := key + " = " + quote(value)
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	web := -1
	for i, l := range lines {
		if isWebHeader(l) {
			web = i
			break
		}
	}

	var b strings.Builder
	switch {
	case web < 0:
		n := len(lines)
		for n > 0 && strings.TrimSpace(lines[n-1]) == "" {
			n--
		}
		for _, l := range lines[:n] {
			b.WriteString(l)
		}
		if n > 0 {
			if lineEnding(lines[n-1]) == "" {
				b.WriteString(nl)
			}
			b.WriteString(nl)
		}
		b.WriteString("[web]" + nl + newLine + nl)
	default:
		end, last := len(lines), web
		for i := web + 1; i < len(lines); i++ {
			if isHeader(lines[i]) {
				end = i
				break
			}
		}
		replaced := -1
		for i := web + 1; i < end; i++ {
			t := strings.TrimSpace(lines[i])
			if t != "" && !strings.HasPrefix(t, "#") {
				last = i
			}
			if replaced < 0 {
				if _, _, ok := assignmentRest(lines[i], key); ok {
					replaced = i
				}
			}
		}
		for i, l := range lines {
			switch {
			case i == replaced:
				s, e, _ := assignmentRest(l, key)
				b.WriteString(l[:s] + quote(value) + l[e:])
			case replaced < 0 && i == last:
				b.WriteString(l)
				if lineEnding(l) == "" {
					b.WriteString(nl)
				}
				b.WriteString(newLine + nl)
			default:
				b.WriteString(l)
			}
		}
	}
	newText = b.String()

	var newRaw rawFile
	if _, derr := toml.Decode(newText, &newRaw); derr != nil {
		return text, errUnsafeWebEdit
	}
	got := &newRaw.Web.Bind
	if key == "public_url" {
		got = &newRaw.Web.PublicURL
	}
	if *got != value {
		return text, errUnsafeWebEdit
	}
	// Blank the edited key on both sides: everything else must be equal.
	*got = ""
	if key == "bind" {
		oldRaw.Web.Bind = ""
	} else {
		oldRaw.Web.PublicURL = ""
	}
	if !reflect.DeepEqual(oldRaw, newRaw) {
		return text, errUnsafeWebEdit
	}
	return newText, nil
}
