package ui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// leaderSetting is what checkLeader decides for the leader key set in
// config.toml's [dash] table.
type leaderSetting struct {
	// name is the string key.Matches and KeyPressMsg.String see for the key
	// to use ("ctrl+o"). It is defaultLeaderKey when the setting is unset or
	// refused.
	name string
	// key is the decoded key the terminal reports for name. The pass-through
	// sends it to the agent when the leader is pressed twice, so
	// tea.KeyPressMsg(key).String() == name.
	key tea.Key
	// configured is the value as written, trimmed.
	configured string
	// refused is the plain-language reason the configured value cannot be
	// used, "" when it was accepted or never set.
	refused string
	// why is refused in a few words ("needs ctrl or alt"), for the header
	// line's shorter forms (leaderHeaderForms), which cannot fit the whole
	// sentence at ordinary widths. "" exactly when refused is.
	why string
}

// leaderModifiers lists the modifiers in the order Key.Keystroke writes them
// (ultraviolet key.go, Keystroke: ctrl, alt, shift, meta, hyper, super). The
// order is part of the check because KeyPressMsg.String() produces exactly one
// spelling per key: "shift+ctrl+a" would parse fine but never match anything
// the dashboard receives.
var leaderModifiers = []struct {
	name string
	mod  tea.KeyMod
}{
	{"ctrl", tea.ModCtrl},
	{"alt", tea.ModAlt},
	{"shift", tea.ModShift},
	{"meta", tea.ModMeta},
	{"hyper", tea.ModHyper},
	{"super", tea.ModSuper},
}

// configKeyNames maps the key names Keystroke can write to their codes. It
// is the keyTypeString map of ultraviolet's key.go minus the keypad, media,
// volume and lone-modifier entries, which a person would not choose as a
// leader and which many terminals never report; those are refused as names
// the dashboard would never see. It is built by function, not held as
// package state, so nothing mutable is shared.
func configKeyNames() map[string]rune {
	m := map[string]rune{
		"enter": tea.KeyEnter, "tab": tea.KeyTab, "backspace": tea.KeyBackspace,
		"esc": tea.KeyEscape, "space": tea.KeySpace,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"begin": tea.KeyBegin, "find": tea.KeyFind, "insert": tea.KeyInsert,
		"delete": tea.KeyDelete, "select": tea.KeySelect,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd,
		"capslock": tea.KeyCapsLock, "scrolllock": tea.KeyScrollLock, "numlock": tea.KeyNumLock,
		"printscreen": tea.KeyPrintScreen, "pause": tea.KeyPause, "menu": tea.KeyMenu,
	}
	// F1..F63 are consecutive codes (ultraviolet's iota block), so one
	// offset reaches each; the round trip in TestCheckLeader proves it.
	for n := 1; n <= 63; n++ {
		m["f"+strconv.Itoa(n)] = tea.KeyF1 + rune(n-1)
	}
	return m
}

// leaderReserved are the keys the dashboard keeps for itself without the
// leader, with what each does. A leader on one of them would either never
// fire or take the key away from its job. Names are in Keystroke spelling.
func leaderReserved() map[string]string {
	return map[string]string{
		"enter":     "opens the selected item",
		"esc":       "goes back and closes things",
		"tab":       "moves between panes",
		"shift+tab": "moves between panes",
		"ctrl+c":    "quits the dashboard",
		"up":        "moves the selection",
		"down":      "moves the selection",
		"left":      "moves between panes",
		"right":     "moves between panes",
		"pgup":      "scrolls",
		"pgdown":    "scrolls",
		"home":      "jumps to the top",
		"end":       "jumps to the bottom",
		"ctrl+u":    "scrolls the detail pane up",
		"ctrl+d":    "scrolls the detail pane down",
		"k":         "moves the list selection up",
		"j":         "moves the list selection down",
	}
}

// checkLeader decides which key the dashboard uses as its leader, given the
// raw [dash] leader value from config.toml. It is pure: the caller reads the
// file, and a refusal never stops the dashboard, it falls back to the default
// defaultLeaderKey and carries the reason for the header line (T9.2, docs/decisions.md).
//
// Rules, in order:
//  1. An empty value is "unset": the default, no reason.
//  2. The value is lower-cased and parsed the way Key.Keystroke writes a key
//     (ultraviolet key.go): modifiers in its fixed order, then a key name from
//     its keyTypeString map or one printable rune. Anything else is a name the
//     dashboard would never see, because KeyPressMsg.String is Key.String,
//     which returns the typed text when there is any and Keystroke otherwise
//     (so "shift+a" arrives as "A", never as "shift+a").
//  3. Keys the dashboard keeps for itself are refused.
//  4. A key with no modifier is refused: a plain key would be typed into the
//     filter or the agent rather than open the actions.
//
// The accepted key is built so tea.KeyPressMsg(key).String() == name.
func checkLeader(configured string) leaderSetting {
	configured = strings.TrimSpace(configured)
	fallback := leaderSetting{name: defaultLeaderKey, key: tea.Key{Code: ']', Mod: tea.ModCtrl}, configured: configured}
	if configured == "" {
		return fallback
	}
	refuse := func(why, format string, args ...any) leaderSetting {
		fallback.refused = fmt.Sprintf(format, args...)
		fallback.why = why
		return fallback
	}
	neverSeen := func() leaderSetting {
		return refuse("not a key name", "%q is not a key name the dashboard would ever see (write it like ctrl+o)", configured)
	}

	parts := strings.Split(strings.ToLower(configured), "+")
	last := parts[len(parts)-1]
	var mod tea.KeyMod
	next := 0 // index into leaderModifiers of the first modifier still allowed
	for _, p := range parts[:len(parts)-1] {
		found := -1
		for i := next; i < len(leaderModifiers); i++ {
			if leaderModifiers[i].name == p {
				found = i
				break
			}
		}
		if found < 0 {
			return neverSeen()
		}
		mod |= leaderModifiers[found].mod
		next = found + 1
	}

	var code rune
	if c, ok := configKeyNames()[last]; ok {
		code = c
	} else if utf8.RuneCountInString(last) == 1 {
		r, _ := utf8.DecodeRuneInString(last)
		if !unicode.IsPrint(r) || unicode.IsUpper(r) || r == ' ' {
			return neverSeen()
		}
		// shift on a printable key arrives as the typed (shifted) text.
		if mod == tea.ModShift {
			return neverSeen()
		}
		code = r
	} else {
		return neverSeen()
	}

	name := strings.Join(parts, "+")
	if what, ok := leaderReserved()[name]; ok {
		return refuse("the dashboard uses it", "%q is a key the dashboard already uses (it %s); pick another, like ctrl+o", configured, what)
	}
	if mod == 0 {
		return refuse("needs ctrl or alt", "%q has no ctrl or alt, so it would be typed rather than open the actions; add ctrl or alt, like ctrl+o", configured)
	}
	return leaderSetting{name: name, key: tea.Key{Code: code, Mod: mod}, configured: configured}
}

// LeaderName is the name of the leader key the dashboard will use for the
// leader written in config.toml, for text printed outside the dashboard (the
// plan commands' messages). A value checkLeader refuses gives the default,
// because that is the key the dashboard falls back to.
func LeaderName(configured string) string {
	return checkLeader(configured).name
}
