package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
)

// dash_notice.go is the one place afk still knows the name config.json. The
// dashboard's search settings used to live in that file; they now live in the
// [dash] table of config.toml (internal/daemonconfig), and nothing reads the
// old file for settings any more. A person who upgraded would otherwise see
// their search folders vanish with no word as to why, so each start of the
// dashboard says once that the old file is no longer read and prints the
// lines to put in config.toml.
//
// The old file is read here for that one purpose. Its values go into the
// printed text and nowhere else: no setting is ever taken from it, and afk
// never writes, renames or deletes either file.

// oldSettingsFileName is the retired settings file. It is looked for only in
// the folder of config.toml: afk has one config folder now, so an old file
// that was once reached some other way (a relative XDG_CONFIG_HOME, which the
// retired reader honoured) is not hunted for.
const oldSettingsFileName = "config.json"

// oldSettingsMinInterval is the shortest scan_interval the [dash] reader
// accepts. It repeats daemonconfig's own floor, which is not exported; the
// notice has to know it so that it never prints a line config.toml would
// then be refused for. TestOldSettingsNotice loads every built block through
// daemonconfig.Load, so a floor that drifts from the reader's fails there.
const oldSettingsMinInterval = time.Second

// oldSettingsNotice builds the two texts shown when a retired config.json is
// still in afk's config folder: full, printed to the terminal before the
// dashboard starts, and short, the one-line toast inside it. Both are empty
// when the file does not exist, which is the rule that matters most here: a
// machine that never had the old file must never see a word about it.
//
// It is pure, in the style of shouldHandOffToDash: the stat and the read
// happen in announceOldSettingsFile and arrive as exists, data and readErr,
// so every shape of old file is a table row with no file on disk.
//
// Only the three search keys are ever echoed. The old file is the owner's,
// may hold anything, and the text goes to a terminal that may be shared or
// logged; that is also why a JSON syntax error is reported without the
// parser's message, which quotes characters of the file.
func oldSettingsNotice(oldPath, newPath string, exists bool, data []byte, readErr error) (full, short string) {
	if !exists {
		return "", ""
	}

	// The short form names both files by name only, not by path. The
	// dashboard's toast is 48 columns wide and shows three lines, so with a
	// full path in it the part that says where the lines are was cut off
	// (seen in a built binary). The full path is in the terminal text.
	short = fmt.Sprintf("afk no longer reads %s. Quit to see the lines to add to %s, printed in the terminal.",
		filepath.Base(oldPath), filepath.Base(newPath))

	var b strings.Builder
	fmt.Fprintf(&b, "afk no longer reads %s.\n", oldPath)
	fmt.Fprintf(&b, "The dashboard's search settings now live in %s.\n", newPath)

	lines, unreadable := oldSettingsLines(data, readErr)
	switch {
	case unreadable != "":
		fmt.Fprintf(&b, "afk could not read the old file (%s), so it cannot work out the lines for you.\n", unreadable)
		b.WriteString("These are the three settings with their defaults. Add the ones you want there, without the leading \"#\":\n\n")
		b.WriteString("[dash]\n")
		b.WriteString("# search_roots = [\"~/code\"]\n")
		// Written in whole seconds because Duration.String gives "1m0s",
		// which is correct and reads like a mistake.
		fmt.Fprintf(&b, "# scan_interval = \"%ds\"\n", int(daemonconfig.DefaultScanInterval/time.Second))
		fmt.Fprintf(&b, "# max_depth = %d\n\n", daemonconfig.DefaultMaxDepth)
		b.WriteString("afk has not changed either file. You can delete the old file once you have added the lines you want.\n")
	case len(lines) == 0:
		b.WriteString("The old file sets none of the three search settings (search_roots, scan_interval, max_depth), so there is nothing to move.\n")
		b.WriteString("afk has not changed either file. You can delete the old file.\n")
	default:
		// A second [dash] header in one file is a TOML error that would stop
		// every afk command reading config.toml, so the text says what to do
		// when the table is already there. The builder is not given
		// config.toml's contents, by design: it reports on the old file only.
		b.WriteString("Add these lines there. If that file already has a [dash] line, put the others under it and do not add a second one.\n\n")
		b.WriteString("[dash]\n")
		for _, line := range lines {
			b.WriteString(line + "\n")
		}
		b.WriteString("\nafk has not changed either file. You can delete the old file once the lines are moved.\n")
	}
	return b.String(), short
}

// oldSettingsLines turns the old file into the lines that go under [dash],
// one per key the file holds, in a fixed order. A value the [dash] reader
// would refuse becomes a "#" line saying which key was left out and why, so
// pasting the block can never produce a config.toml that reports an error.
//
// unreadable is non-empty, in plain words, when no lines can be built at all:
// the read failed or the text is not a JSON object.
func oldSettingsLines(data []byte, readErr error) (lines []string, unreadable string) {
	if readErr != nil {
		return nil, readErr.Error()
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		// raw == nil is the text "null": valid JSON, but not a settings file.
		return nil, "it is not valid JSON"
	}

	if v, ok := presentKey(raw, "search_roots"); ok {
		var roots []string
		if err := json.Unmarshal(v, &roots); err != nil {
			lines = append(lines, `# search_roots is left out: the old file does not hold it as a list of folders in quotes. Write it like ["~/code"].`)
		} else {
			quoted := make([]string, len(roots))
			for i, root := range roots {
				quoted[i] = tomlString(root)
			}
			lines = append(lines, "search_roots = ["+strings.Join(quoted, ", ")+"]")
		}
	}

	if v, ok := presentKey(raw, "scan_interval"); ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			lines = append(lines, `# scan_interval is left out: the old file does not hold it as text in quotes. Write it like "60s".`)
		} else if trimmed := strings.TrimSpace(s); trimmed == "" {
			// The [dash] reader takes an empty interval as the default, as
			// the old reader did, so the line moves as written.
			lines = append(lines, "scan_interval = "+tomlString(s))
		} else if d, err := time.ParseDuration(trimmed); err != nil {
			lines = append(lines, fmt.Sprintf(`# scan_interval is left out: %s is not a length of time. Write it like "60s".`, tomlString(s)))
		} else if d < oldSettingsMinInterval {
			lines = append(lines, fmt.Sprintf(`# scan_interval is left out: %s is under one second, the shortest afk accepts. Write "1s" or more.`, tomlString(s)))
		} else {
			lines = append(lines, "scan_interval = "+tomlString(s))
		}
	}

	if v, ok := presentKey(raw, "max_depth"); ok {
		var n int64
		if err := json.Unmarshal(v, &n); err != nil {
			lines = append(lines, "# max_depth is left out: the old file does not hold it as a whole number. Write it like 3, with no quotes.")
		} else if n < 1 || int64(int(n)) != n {
			// The second test is the [dash] reader's own: a number too big
			// for int on a 32-bit build is refused there.
			lines = append(lines, fmt.Sprintf("# max_depth is left out: %d is not a depth afk accepts. Write a whole number of 1 or more, such as 3.", n))
		} else {
			lines = append(lines, fmt.Sprintf("max_depth = %d", n))
		}
	}
	return lines, ""
}

// presentKey reports a key the old file really sets. A JSON null counts as
// absent, as it did for the old reader (encoding/json leaves the field alone),
// so a null does not produce a line about a value nobody wrote.
func presentKey(raw map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	v, ok := raw[key]
	if !ok || strings.TrimSpace(string(v)) == "null" {
		return nil, false
	}
	return v, true
}

// tomlString writes s as a TOML basic string.
//
// strconv.Quote is not used although it looks like the same thing: it writes
// control characters as \x07 or \a and \v, and TOML 1.0 basic strings have
// neither escape (only \b \t \n \f \r \" \\ and \uXXXX), so a folder name with
// such a character would make the pasted line a syntax error. Everything at or
// above a space is written as it is, which is valid because the text comes
// out of encoding/json and is therefore valid UTF-8.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// oldSettingsSource is everything announceOldSettingsFile reads from the
// machine, as fields so a test can stand in for the disk.
type oldSettingsSource struct {
	// configPath is the path of config.toml, empty when it cannot be worked
	// out (no home folder). The old file is looked for beside it.
	configPath string
	stat       func(string) (fs.FileInfo, error)
	readFile   func(string) ([]byte, error)
}

// realOldSettingsSource is the source `afk dash` uses.
func realOldSettingsSource() oldSettingsSource {
	return oldSettingsSource{
		configPath: daemonconfig.Path(daemonconfig.DefaultDeps()),
		stat:       os.Stat,
		readFile:   os.ReadFile,
	}
}

// announceOldSettingsFile is the impure half: it looks for the retired file
// beside config.toml, prints the full notice to w when it is there, and
// returns the short form for the dashboard's toast (empty when there is
// nothing to say).
//
// A failed stat counts as "no file", whatever the error. When the folder
// cannot be looked into there is no telling whether the file is there, and
// the notice must never be shown without it. A read that fails after a good
// stat is different: the file is known to be there, so the notice is still
// shown and says the file could not be read. A folder of that name is not the
// old settings file and is passed over.
func announceOldSettingsFile(w io.Writer, src oldSettingsSource) string {
	if src.configPath == "" {
		return ""
	}
	oldPath := filepath.Join(filepath.Dir(src.configPath), oldSettingsFileName)
	info, err := src.stat(oldPath)
	exists := err == nil && !info.IsDir()

	var data []byte
	var readErr error
	if exists {
		data, readErr = src.readFile(oldPath)
	}
	full, short := oldSettingsNotice(oldPath, src.configPath, exists, data, readErr)
	if full != "" {
		// The blank line keeps the notice apart from whatever the terminal
		// shows next once the dashboard has quit.
		fmt.Fprint(w, full+"\n")
	}
	return short
}
