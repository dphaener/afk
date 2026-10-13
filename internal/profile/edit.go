package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// This file is the only writer of profile.toml, and the first TOML writer in
// the repository. It never re-encodes the file. The TOML library's encoder
// writes a struct, so it would drop every comment the owner wrote and every
// key this version does not know; the profile is a hand-edited file whose
// comments are part of what the owner said. So Set edits the text of one
// [[entry]] block and leaves every other byte alone, and then proves the edit
// by parsing the result (see the guard in Set).

// Template is the text `afk profile init` writes. Every example is commented
// out, so a fresh template parses as an empty profile with no problems and the
// owner switches on only what they mean. The values are placeholders that
// belong to nobody (example.com, "an example ..."): a template that shipped
// one person's real choices would be read by a plan session as the owner's.
//
// The tests rely on one convention here: an example line is "# " followed by
// TOML ([[entry]], or a key and " = "), and no line of prose starts that way.
func Template() string {
	return `# afk owner profile
#
# How you like to build and host things, stated once. Every planning session
# reads this file, so it asks you less and never has to guess.
#
# Each [[entry]] has a topic, a value and a strength:
#   strength = "rule"     must be followed
#   strength = "default"  used unless the project says otherwise
# Leave strength out and the entry is a default.
#
# kind narrows an entry to one kind of project: "web-app", "cli", "library",
# or any word of your own. An entry with a kind beats a general entry on the
# same topic for that kind of project.
#
# A value of "no preference" means: recommend one and say why.
#
# reason is optional. It says why, which helps when a project is unusual.
#
# Never put keys, tokens or passwords in this file. afk refuses a value that
# looks like one. Secrets belong in the environment, not here.
#
# Change one entry with:  afk profile set <topic> <value> [--kind K] [--rule]
# Check the file with:    afk profile check
# To use an example below, remove the "# " from the start of its lines.

# Documents that hold your house rules. They are read, not copied, so a change
# to a document is picked up the next time a plan is made.
# rule_files = ["~/path/to/house-rules.md"]

# Where new projects are created.
# [[entry]]
# topic = "project location"
# value = "~/code"
# strength = "default"

# [[entry]]
# topic = "language and framework"
# value = "an example language with an example framework"
# strength = "default"
# kind = "web-app"

# [[entry]]
# topic = "database"
# value = "an example database"
# strength = "rule"
# reason = "one kind of database to run and back up"

# [[entry]]
# topic = "auth"
# value = "no preference"
# strength = "default"

# [[entry]]
# topic = "tests"
# value = "tests are written with the code and run before every merge"
# strength = "rule"

# [[entry]]
# topic = "interface components"
# value = "an example component library"
# strength = "default"
# reason = "a starting point, not a strict rule"

# [[entry]]
# topic = "CI"
# value = "an example CI service"
# strength = "default"

# [[entry]]
# topic = "hosting and deploy"
# value = "one small server, deployed with an example deploy tool"
# strength = "default"
# kind = "web-app"

# [[entry]]
# topic = "storage"
# value = "an example file storage service"
# strength = "default"

# [[entry]]
# topic = "email"
# value = "sent from mail.example.com through an example email service"
# strength = "default"

# [[entry]]
# topic = "domain names"
# value = "a subdomain of example.com for each project"
# strength = "default"

# How you want to be told things. A notification topic address works as a
# password (anyone who knows it can read and send your messages), so it stays
# out of this file. Say only which service you use.
# [[entry]]
# topic = "notifications"
# value = "an example push notification service"
# strength = "default"

# [[entry]]
# topic = "design tool"
# value = "no preference"
# strength = "default"

# Which accounts to use. Name the service, never the login.
# [[entry]]
# topic = "accounts"
# value = "use the accounts I already have, and ask before opening a new one"
# strength = "rule"
`
}

// setHeader starts a profile that `afk profile set` creates from nothing. It
// is short on purpose: the full template belongs to `afk profile init`, and an
// owner who typed one `set` asked for one entry, not for a page of examples.
const setHeader = "# afk owner profile: how you like to build and host things. No keys or tokens here.\n" +
	"# Change an entry with `afk profile set`, or edit this file by hand.\n" +
	"# `afk profile init` shows every topic (it writes a template when there is no profile yet).\n"

// errUnsafeEdit is what Set returns when it cannot prove its own edit.
var errUnsafeEdit = errors.New("afk could not edit the file safely, so nothing was changed; change this entry by hand in an editor")

// Set adds the entry e to profile text, or replaces the entry with the same
// topic (by TopicKey) and exactly the same kind. It is pure: text in, text
// out. e.Strength and e.Reason are final; the caller decides what to keep
// from an entry that is being replaced. An empty e.Strength is written as
// StrengthDefault, which is how Parse would read it anyway.
//
// The edit is made on the text, not by re-encoding, because the encoder would
// delete the owner's comments and unknown keys. Only the lines of the one
// block change: from its [[entry]] line to its last line that is neither
// blank nor a comment. Comment and blank lines after that are left alone,
// since they usually introduce whatever comes next.
//
// The block is found by reading lines, and lines can lie: a line reading
// [[entry]] inside a multi-line string is not a header. So the result is
// parsed again and must be exactly the old profile with this one entry
// replaced or appended, with the same unknown keys. If it is not, Set returns
// the original text and an error. That guard is what makes the line scan
// safe: a file it misreads is refused, never edited wrongly.
//
// A field that looks like a key or token is refused here as well as in the
// command, so no caller can write one. The message never repeats the text.
func Set(text string, e Entry) (newText string, replaced bool, err error) {
	if e.Strength == "" {
		e.Strength = StrengthDefault
	}
	fields := []struct{ name, val string }{
		{"topic", e.Topic}, {"value", e.Value}, {"strength", e.Strength}, {"kind", e.Kind}, {"reason", e.Reason},
	}
	// Checked before anything else: a line break cannot be written as a
	// one-line "key = value", and one line per key is what keeps a later
	// edit of this block simple.
	for _, f := range fields {
		if strings.ContainsAny(f.val, "\n\r") {
			return text, false, fmt.Errorf("the %s has a line break in it; put it on one line", f.name)
		}
	}
	for _, f := range fields {
		if why, bad := LooksSecret(f.val); bad {
			return text, false, fmt.Errorf("the %s looks like a key or token (%s), so nothing was saved; keep keys and tokens out of the profile", f.name, why)
		}
	}

	old, oldUnknown, perr := Parse([]byte(text))
	if perr != nil {
		return text, false, fmt.Errorf("the profile has a mistake in it, fix it first: %w", perr)
	}

	// Keep the file's own line endings so an edit does not leave it mixed.
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	block := renderEntry(e)
	lines := strings.SplitAfter(text, "\n")

	want := Profile{RuleFiles: old.RuleFiles, Entries: slices.Clone(old.Entries)}
	idx, found := Find(old, e.Topic, e.Kind)
	if found {
		start, end, ok := entryBlock(lines, idx)
		if !ok {
			return text, false, errUnsafeEdit
		}
		// The last new line takes the ending of the last old line, which is
		// empty when the block ran to the end of a file with no final newline.
		tail := lineEnding(lines[end])
		var b strings.Builder
		for _, l := range lines[:start] {
			b.WriteString(l)
		}
		b.WriteString(strings.Join(block, nl))
		b.WriteString(tail)
		for _, l := range lines[end+1:] {
			b.WriteString(l)
		}
		newText = b.String()
		want.Entries[idx] = e
	} else {
		// Drop trailing blank lines so there is exactly one before the block.
		n := len(lines)
		for n > 0 && strings.TrimSpace(lines[n-1]) == "" {
			n--
		}
		var b strings.Builder
		if n == 0 {
			b.WriteString(setHeader)
		} else {
			for _, l := range lines[:n] {
				b.WriteString(l)
			}
			if lineEnding(lines[n-1]) == "" {
				b.WriteString(nl)
			}
		}
		b.WriteString(nl)
		b.WriteString(strings.Join(block, nl))
		b.WriteString(nl)
		newText = b.String()
		want.Entries = append(want.Entries, e)
	}

	got, gotUnknown, gerr := Parse([]byte(newText))
	if gerr != nil ||
		!slices.Equal(got.RuleFiles, want.RuleFiles) ||
		!slices.Equal(got.Entries, want.Entries) ||
		!slices.Equal(gotUnknown, oldUnknown) {
		return text, false, errUnsafeEdit
	}
	return newText, found, nil
}

// renderEntry returns the lines of one [[entry]] block, without line endings.
// kind and reason are written only when set, so a general entry stays short.
func renderEntry(e Entry) []string {
	out := []string{
		"[[entry]]",
		"topic = " + quote(e.Topic),
		"value = " + quote(e.Value),
		"strength = " + quote(e.Strength),
	}
	if e.Kind != "" {
		out = append(out, "kind = "+quote(e.Kind))
	}
	if e.Reason != "" {
		out = append(out, "reason = "+quote(e.Reason))
	}
	return out
}

// quote writes s as a TOML basic string: backslash and double quote are
// escaped, and so is every control character, which TOML does not allow raw
// in a basic string (tab is allowed raw but is escaped too, so it stays
// visible in an editor).
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// entryBlock finds the n-th (0-based) [[entry]] block in lines and returns
// the index of its header line and of the last line of its body. The body
// runs to the last line, before the next table header or the end, that is
// neither blank nor a comment. ok is false when there are fewer than n+1
// blocks, which happens when entries are written in a form this scan does not
// read (an inline array, a quoted table name).
//
// This reads lines, not TOML, so it can be fooled by a multi-line string.
// Set's guard catches that; do not use this without the guard.
func entryBlock(lines []string, n int) (start, end int, ok bool) {
	seen := -1
	for i := 0; i < len(lines); i++ {
		if !isEntryHeader(lines[i]) {
			continue
		}
		seen++
		if seen != n {
			continue
		}
		end = i
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if strings.HasPrefix(t, "[") {
				break
			}
			if t != "" && !strings.HasPrefix(t, "#") {
				end = j
			}
		}
		return i, end, true
	}
	return 0, 0, false
}

// isEntryHeader reports whether a line is an [[entry]] table header, allowing
// spaces inside the brackets and a comment after them, both of which TOML
// accepts.
func isEntryHeader(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[[") {
		return false
	}
	closeAt := strings.Index(t, "]]")
	if closeAt < 0 || strings.TrimSpace(t[2:closeAt]) != "entry" {
		return false
	}
	rest := strings.TrimSpace(t[closeAt+2:])
	return rest == "" || strings.HasPrefix(rest, "#")
}

// lineEnding returns the "\n" or "\r\n" a line ends with, or "".
func lineEnding(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	}
	return ""
}

// maxLinkHops bounds the walk through symlinks so a loop of links ends in an
// error instead of spinning. 40 is the limit Linux itself uses.
const maxLinkHops = 40

// resolveLink follows path while it is a symlink and returns the file the
// links end at, which may not exist yet. filepath.EvalSymlinks is not used
// because it fails on a link whose target is missing, and that is a real
// case: a dotfiles link made before the profile was first written.
func resolveLink(path string) (string, error) {
	for range maxLinkHops {
		fi, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return path, nil
			}
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	return "", fmt.Errorf("%s: too many links to follow", path)
}

// Write replaces the file at path with data, so that a reader sees either the
// old text or the new text and never half of one.
//
// The link is followed: when path is a symlink the file it points at is
// written, and the link stays a link. An owner may keep the profile in a
// dotfiles repository and link it into the config folder; a rename over the
// link would replace the link with a plain file and quietly cut the
// repository off from the file afk reads.
//
// The temp file is made in the same folder as the file it replaces because a
// rename is atomic only within one file system; a temp file in the system
// temp folder could be on another one, where rename fails or copies.
//
// The mode is set on purpose. os.CreateTemp makes its file 0600 whatever was
// there before, so without the chmod a profile the owner had made readable
// (0644, say, in a shared dotfiles checkout) would silently turn private on
// the first `afk profile set`. A new file is 0600 and its folder 0700: the
// profile is not secret, but it is nobody else's business either.
func Write(path string, data []byte) error {
	target, err := resolveLink(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(target); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("%s is a folder, not a file", target)
		}
		mode = fi.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, target); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// Create writes a new file at path and refuses when anything is already
// there, with an error for which errors.Is(err, fs.ErrExist) is true. It is
// for `afk profile init`, which must never overwrite a profile.
//
// O_EXCL makes the check and the creation one step, so two runs at once
// cannot both win, which a "stat, then write" would allow. With O_EXCL the
// kernel also refuses a symlink at path, even a dangling one (open(2):
// "If O_EXCL and O_CREAT are set, and path names a symbolic link, open()
// shall fail"), so init does not write through a link somebody set up.
func Create(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		// The file is ours (O_EXCL made it), so removing a half-written one
		// cannot destroy anything, and it lets a second init succeed.
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
