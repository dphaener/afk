package prompt

import (
	"charm.land/huh/v2"
)

// Description prompts for an optional multi-line description and returns it.
// Empty input is valid and returns ("", nil). The Text field defaults to
// allowing an external editor (ctrl+e launches nano/EDITOR) — this explicitly
// disables it via ExternalEditor(false) so the user stays in the prompt.
//
// Key bindings differ from the dashboard's own description field: here (huh's
// Text, keymap.go) ENTER submits the form and a newline is alt+enter or ctrl+j;
// in the dashboard (bubbles/textarea) ENTER inserts a newline and submission
// happens from another field. The divergence is inherent to the two widget
// sets — each ships its own keymap — and cannot be reconciled at the call site.
//
// A Ctrl-C abort returns a non-nil error (huh.ErrUserAborted); callers treat
// that as a fatal cancel. Assumes an interactive terminal (see package doc).
func Description(prompt string) (string, error) {
	var desc string
	field := huh.NewText().
		Title(prompt).
		Value(&desc).
		ExternalEditor(false)

	form := huh.NewForm(huh.NewGroup(field)).
		WithTheme(themeFunc()).
		WithAccessible(accessible())

	if err := form.Run(); err != nil {
		return "", err
	}
	return desc, nil
}
