// Package prompt wraps charmbracelet/huh into a small set of one-shot,
// pre-themed interactive prompts (a base-branch picker, a yes/no confirm, and a
// validated name input) used by the afk CLI.
//
// Contract: these functions assume they are called on an interactive terminal.
// TTY detection is the caller's responsibility (it lives caller-side in package
// main); prompt performs none. On a non-interactive stdin huh will fail rather
// than block, but callers are expected to gate these calls behind their own TTY
// check and fall back to non-interactive behavior.
//
// Cancel semantics: huh v1.0.0 binds abort to Ctrl-C only (Esc is a
// filter/close key inside Select, not an abort). A Ctrl-C abort surfaces as a
// non-nil error (huh.ErrUserAborted) from every function here, which callers
// treat as a fatal cancel.
//
// Output stream: huh renders the interactive form to stderr by default, so
// prompts do not corrupt piped stdout. (In accessible mode huh writes to
// stdout instead — see accessible().)
//
// The package is a leaf: it depends only on huh, lipgloss, and internal/palette.
// Validators (e.g. session-name validation) are passed in as funcs so prompt
// never imports internal/session.
package prompt

import (
	"os"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/palette"
)

// accessible reports whether huh should render in accessible (plain,
// sequential, screen-reader-friendly) mode. This mirrors the GitHub CLI
// pattern: opt in via the ACCESSIBLE environment variable. huh v1.0.0 only
// auto-enables accessible mode for TERM=dumb, not for this env var, so the
// explicit wiring here is required.
func accessible() bool {
	return os.Getenv("ACCESSIBLE") != ""
}

// Theme builds the huh style set used by every prompt in this package. It
// starts from huh.ThemeBase() and overrides the Focused/Blurred field style
// slots with colors from internal/palette so prompts read as part of the same
// visual system as the Bubble Tea dashboard.
//
// huh v2 renamed the v1 *huh.Theme struct to *huh.Styles and repurposed the
// name Theme for an interface (`Theme(isDark bool) *Styles`) that only
// huh.ThemeFunc satisfies directly. Theme() still returns *huh.Styles (not
// the interface) so its fields stay directly assertable — prompt_test.go
// reaches into th.Focused.Title etc. on the return value. themeFunc (below)
// is the one adapter call sites hand to WithTheme. See decisions.md
// finding (c).
//
// isDark is hardcoded true: afk's palette (internal/palette) is a
// dark-terminal palette with no light-mode variant, so there is no real
// light/dark switch to plumb through yet.
//
// Palette -> huh style-slot mapping (see decisions.md for rationale):
//   - palette.Emphasis     -> field/group Title (matches the dashboard panel titles)
//   - palette.Muted        -> Description, blurred button fg, text placeholder
//   - palette.Error        -> ErrorIndicator, ErrorMessage
//   - palette.HeaderAccent -> Select selector/indicators, focused field border,
//     Confirm focused button bg, text-input cursor/prompt (the dashboard header accent)
//   - palette.Success      -> selected option (multiselect; harmless for single Select)
//   - palette.White        -> focused button fg
//   - palette.SelectionBg  -> blurred button bg (mirrors the dashboard row-selection bg)
func Theme() *huh.Styles {
	t := huh.ThemeBase(true)

	var (
		emphasis = lipgloss.Color(palette.Emphasis)
		muted    = lipgloss.Color(palette.Muted)
		errColor = lipgloss.Color(palette.Error)
		accent   = lipgloss.Color(palette.HeaderAccent)
		success  = lipgloss.Color(palette.Success)
		white    = lipgloss.Color(palette.White)
		selBg    = lipgloss.Color(palette.SelectionBg)
	)

	// Focused field styles.
	t.Focused.Base = t.Focused.Base.BorderForeground(accent)
	t.Focused.Card = t.Focused.Base
	t.Focused.Title = t.Focused.Title.Foreground(emphasis).Bold(true)
	t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(emphasis).Bold(true)
	t.Focused.Description = t.Focused.Description.Foreground(muted)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(errColor)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(errColor)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(accent)
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(accent)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(accent)
	t.Focused.Option = t.Focused.Option.Foreground(white)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(success)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(white).Background(accent).Bold(true)
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(muted).Background(selBg)

	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(accent)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(accent)
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(muted)

	// Blurred styles inherit from Focused, then hide the border and clear the
	// paging indicators (mirrors huh's own built-in themes).
	t.Blurred = t.Focused
	t.Blurred.Base = t.Blurred.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()

	// Group chrome tracks the focused field styles.
	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description

	return t
}

// themeFunc adapts Theme to the huh.Theme interface for WithTheme, ignoring
// isDark for the reason Theme hardcodes it. It is the single adapter every
// prompt here uses, so the closure isn't restated per call site.
func themeFunc() huh.Theme {
	return huh.ThemeFunc(func(bool) *huh.Styles { return Theme() })
}
