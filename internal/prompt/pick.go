package prompt

import (
	"charm.land/huh/v2"
)

// buildOptions turns the caller's option strings into huh Select options,
// marking the one equal to dflt as pre-selected. Using each string as both the
// display key and the bound value keeps the picker's returned value identical
// to the caller's option strings. Options are built statically (never via
// huh's OptionsFunc, which has a default-value bug: huh#444).
//
// This is a pure function with no TTY dependency so it can be unit-tested
// directly.
func buildOptions(options []string, dflt string) []huh.Option[string] {
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opt := huh.NewOption(o, o)
		if o == dflt {
			opt = opt.Selected(true)
		}
		opts[i] = opt
	}
	return opts
}

// PickBase presents an interactive single-choice picker for the base branch and
// returns the chosen option. The signature matches session.Deps.PickBase
// exactly so it can be wired in as that seam.
//
// dflt is pre-selected: it is both marked on its option and used to
// pre-populate the bound value, so the picker renders with dflt highlighted. If
// dflt is not among options, the first option is highlighted.
//
// A Ctrl-C abort returns a non-nil error (huh.ErrUserAborted); callers treat
// that as a fatal cancel. Assumes an interactive terminal (see package doc).
func PickBase(prompt string, options []string, dflt string) (string, error) {
	value := dflt
	sel := huh.NewSelect[string]().
		Title(prompt).
		Options(buildOptions(options, dflt)...).
		Value(&value)

	form := huh.NewForm(huh.NewGroup(sel)).
		WithTheme(themeFunc()).
		WithAccessible(accessible())

	if err := form.Run(); err != nil {
		return "", err
	}
	return value, nil
}
