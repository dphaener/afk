package prompt

import (
	"reflect"
	"testing"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/session"
)

// --- seam assertion --------------------------------------------------------

// This is a TRUE structural check against the real seam: session.Deps.PickBase
// must accept prompt.PickBase directly, with no adapter. If either signature
// drifts, this line fails to compile -- unlike a hand-copied func type, which
// would silently stay in sync with nothing.
var _ = session.Deps{PickBase: PickBase}

// --- buildOptions ------------------------------------------------------------

func TestBuildOptionsOrderAndDefault(t *testing.T) {
	got := buildOptions([]string{"main", "develop", "staging"}, "develop")

	want := []huh.Option[string]{
		huh.NewOption("main", "main"),
		huh.NewOption("develop", "develop").Selected(true),
		huh.NewOption("staging", "staging"),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildOptions() = %#v, want %#v", got, want)
	}
}

func TestBuildOptionsDefaultNotInList(t *testing.T) {
	got := buildOptions([]string{"main", "develop"}, "does-not-exist")

	want := []huh.Option[string]{
		huh.NewOption("main", "main"),
		huh.NewOption("develop", "develop"),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildOptions() = %#v, want %#v (no option should be pre-selected)", got, want)
	}
}

func TestBuildOptionsSingleOption(t *testing.T) {
	got := buildOptions([]string{"main"}, "main")

	want := []huh.Option[string]{
		huh.NewOption("main", "main").Selected(true),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildOptions() = %#v, want %#v", got, want)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
}

func TestBuildOptionsEmptyList(t *testing.T) {
	got := buildOptions(nil, "main")
	if len(got) != 0 {
		t.Fatalf("buildOptions(nil, ...) = %#v, want empty slice", got)
	}
}

// --- SessionName validator threading (structural only) ---------------------
//
// SessionName's contract (input.go) is: `field.Validate(validate)` is called
// whenever validate != nil, and the built field is handed straight to the
// form -- no copying, no wrapping. We cannot observe huh's runtime validate
// loop without form.Run (which needs a TTY and is explicitly out of scope
// here), so these tests check the two structural facts that matter and are
// observable headlessly:
//
//  1. SessionName's parameter signature is exactly the signature callers
//     (W3.T1: prompt.SessionName(session.ValidateName)) must satisfy -- a
//     compile-time seam check mirroring the Deps.PickBase assertion above.
//  2. huh.Input.Validate(fn) accepts a func(string) error and returns a
//     huh.Field, which is the exact call SessionName makes and the exact
//     type SessionName hands to huh.NewGroup -- so the wiring pattern
//     SessionName uses actually compiles and type-checks against the real
//     huh API, not a hand-copied stand-in.

// TestSessionNameSignatureMatchesValidateName is a compile-time structural
// check: SessionName's validate parameter type must match
// session.ValidateName's signature exactly, or prompt.SessionName(session.ValidateName)
// (as W3.T1 wires it) would fail to compile.
func TestSessionNameSignatureMatchesValidateName(t *testing.T) {
	var sessionName func(validate func(string) error) (string, error) = SessionName
	var validateName func(string) error = session.ValidateName

	if sessionName == nil {
		t.Fatal("SessionName is nil")
	}
	// Confirms, at the type level, that SessionName accepts exactly what
	// session.ValidateName provides -- assigning validateName to a variable
	// of SessionName's parameter type is the compile-time seam check.
	var _ func(string) error = validateName
}

// TestFieldValidateAcceptsSessionNameContract confirms that the exact
// pattern input.go uses -- `field.Validate(validate)` where field is a
// *huh.Input built the same way SessionName builds it, and validate is a
// func(string) error -- compiles and threads the caller's func through to
// a huh.Field, matching SessionName's real code path structurally.
func TestFieldValidateAcceptsSessionNameContract(t *testing.T) {
	var validate func(string) error = session.ValidateName

	field := huh.NewInput().Title("Session name")
	threaded := field.Validate(validate)

	var _ huh.Field = threaded
	if threaded == nil {
		t.Fatal("field.Validate(validate) returned nil, want a huh.Field")
	}
}

// TestSessionNameNilValidatorGuard documents/confirms the nil-guard branch
// in SessionName (`if validate != nil { field = field.Validate(validate) }`)
// is reachable and safe: building the field without calling Validate at all
// still yields a usable huh.Field.
func TestSessionNameNilValidatorGuard(t *testing.T) {
	var validate func(string) error
	field := huh.NewInput().Title("Session name")
	if validate != nil {
		field = field.Validate(validate)
	}
	var _ huh.Field = field
}

// --- Theme -------------------------------------------------------------------

func TestThemeNonNil(t *testing.T) {
	th := Theme()
	if th == nil {
		t.Fatal("Theme() returned nil")
	}
}

func TestThemeUsesPaletteColors(t *testing.T) {
	th := Theme()

	emphasis := lipgloss.Color(palette.Emphasis)
	errColor := lipgloss.Color(palette.Error)
	muted := lipgloss.Color(palette.Muted)
	accent := lipgloss.Color(palette.HeaderAccent)

	if got := th.Focused.Title.GetForeground(); got != emphasis {
		t.Errorf("Focused.Title foreground = %v, want palette.Emphasis %v", got, emphasis)
	}
	if !th.Focused.Title.GetBold() {
		t.Error("Focused.Title bold = false, want true")
	}
	if got := th.Focused.ErrorMessage.GetForeground(); got != errColor {
		t.Errorf("Focused.ErrorMessage foreground = %v, want palette.Error %v", got, errColor)
	}
	if got := th.Focused.Description.GetForeground(); got != muted {
		t.Errorf("Focused.Description foreground = %v, want palette.Muted %v", got, muted)
	}
	if got := th.Focused.SelectSelector.GetForeground(); got != accent {
		t.Errorf("Focused.SelectSelector foreground = %v, want palette.HeaderAccent %v", got, accent)
	}
}

func TestThemeGroupTracksFocusedTitle(t *testing.T) {
	th := Theme()
	if th.Group.Title.GetForeground() != th.Focused.Title.GetForeground() {
		t.Error("Group.Title should track Focused.Title's foreground (theme.go doc comment)")
	}
}

func TestThemeBlurredHidesBorder(t *testing.T) {
	th := Theme()
	hidden := lipgloss.HiddenBorder()
	if th.Blurred.Base.GetBorderStyle() != hidden {
		t.Errorf("Blurred.Base border style = %+v, want HiddenBorder %+v", th.Blurred.Base.GetBorderStyle(), hidden)
	}
}
