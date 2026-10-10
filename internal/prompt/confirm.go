package prompt

import (
	"charm.land/huh/v2"
)

// Confirm presents an interactive yes/no prompt and returns the user's choice.
// title is the question; affirmative and negative label the two buttons (e.g.
// "Delete" / "Cancel").
//
// A Ctrl-C abort returns a non-nil error (huh.ErrUserAborted); callers treat
// that as a cancel and abort with no side effects. Selecting the negative
// button returns (false, nil). Assumes an interactive terminal (see package
// doc).
func Confirm(title, affirmative, negative string) (bool, error) {
	var confirmed bool
	field := huh.NewConfirm().
		Title(title).
		Affirmative(affirmative).
		Negative(negative).
		Value(&confirmed)

	form := huh.NewForm(huh.NewGroup(field)).
		WithTheme(themeFunc()).
		WithAccessible(accessible())

	if err := form.Run(); err != nil {
		return false, err
	}
	return confirmed, nil
}
