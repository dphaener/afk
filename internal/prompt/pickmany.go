package prompt

import (
	"charm.land/huh/v2"
)

// PickMany presents an interactive multi-choice picker and returns the chosen
// options in the order given. Nothing is pre-selected: the caller is asking
// for consent, so an unchanged submit picks nothing.
//
// A Ctrl-C abort returns a non-nil error (huh.ErrUserAborted); callers treat
// that as a cancel. Assumes an interactive terminal (see package doc).
func PickMany(title, description string, options []string) ([]string, error) {
	var picked []string
	sel := huh.NewMultiSelect[string]().
		Title(title).
		Description(description).
		Options(huh.NewOptions(options...)...).
		Value(&picked)

	form := huh.NewForm(huh.NewGroup(sel)).
		WithTheme(themeFunc()).
		WithAccessible(accessible())

	if err := form.Run(); err != nil {
		return nil, err
	}
	// huh records selections in click order; return them in option order so
	// installs run in a stable sequence.
	chosen := make(map[string]bool, len(picked))
	for _, p := range picked {
		chosen[p] = true
	}
	var out []string
	for _, o := range options {
		if chosen[o] {
			out = append(out, o)
		}
	}
	return out, nil
}
