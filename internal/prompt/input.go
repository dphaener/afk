package prompt

import (
	"charm.land/huh/v2"
)

// SessionName prompts for a session name and returns it. Validation is supplied
// by the caller (callers pass session.ValidateName) so this package stays a
// leaf and does not reimplement kebab-case validation. When validate rejects
// the entered value, huh re-prompts inline (showing the returned error) and
// does not submit until the value is valid.
//
// A Ctrl-C abort returns a non-nil error (huh.ErrUserAborted); callers treat
// that as a fatal cancel. Assumes an interactive terminal (see package doc).
func SessionName(validate func(string) error) (string, error) {
	var name string
	field := huh.NewInput().
		Title("Session name").
		Value(&name)
	if validate != nil {
		field = field.Validate(validate)
	}

	form := huh.NewForm(huh.NewGroup(field)).
		WithTheme(themeFunc()).
		WithAccessible(accessible())

	if err := form.Run(); err != nil {
		return "", err
	}
	return name, nil
}
