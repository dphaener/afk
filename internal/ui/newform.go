// newform.go implements the in-TUI session-creation form (the `n`
// keybinding, W7.T1): a five-field creation form on bubbles components.
// Name uses bubbles/textinput, description uses bubbles/textarea, base uses
// bubbles/list with filtering, and the Autonomous checkbox and the Provider
// selector are plain values with no component behind them. It replaces the old
// suspend-and-exec `afk new` shell-out (which recursed into a second
// nested dashboard when the newly-created session itself landed on a TTY).
//
// Validation is delegated entirely to session.ValidateName (internal/session)
// — the canonical kebab-case identity rule the branch, worktree, and plan
// slug all share. This package never re-implements that regex. The
// provider/mode rule (codex needs --autonomous) is likewise delegated to
// provider.CheckMode, so the form refuses exactly what `afk new` refuses,
// with the same text.
package ui

import (
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
)

// newForm is the creation form's pure state. CRITICAL: the zero value is NOT
// safe — textarea.Model.View() and list.Model.Update() both nil-deref on a
// zero-value component. Always construct via newNewForm() or gate every
// component access on formOpen. err holds the inline validation message and
// is cleared by editing the name (formComponentKey), by changing the
// Autonomous checkbox or the Provider selector, or by a successful validate.
// autonomous is the checkbox; it becomes `afk new --autonomous`. provider is
// an index into provider.Names(); a non-default choice becomes
// `afk new --provider <name>`.
type newForm struct {
	nameInput   textinput.Model
	description textarea.Model
	baseList    list.Model
	autonomous  bool
	provider    int
	focusIndex  int
	err         string
}

// The form's focus indices, in tab order, and their count. Every site that
// branches on focusIndex (moveFocus, formComponentKey, submitFormKey, the
// default-message router, cancelForm, formBaseFiltering, renderNewFormModal)
// names these rather than a bare number, so adding a field is a compile-time
// search for formFieldCount instead of a hunt for every literal 4.
const (
	formFocusName = iota
	formFocusDescription
	formFocusBase
	formFocusAutonomous
	formFocusProvider
	formFieldCount
)

// defaultProviderIndex is provider.Default's position in provider.Names(), the
// form's initial selection. Resolved by name rather than assumed to be 0 so
// reordering the provider registry cannot silently change what an untouched
// form creates.
func defaultProviderIndex() int {
	for i, n := range provider.Names() {
		if n == provider.Default {
			return i
		}
	}
	return 0
}

type baseItem string

func (i baseItem) FilterValue() string { return string(i) }
func (i baseItem) Title() string       { return string(i) }
func (i baseItem) Description() string { return "" }

func newNewForm(stack stackState, plainBranches []string, plainTrunk string) newForm {
	nameIn := textinput.New()
	nameIn.Focus()
	nameIn.Placeholder = "session-name"
	nameIn.CharLimit = 128

	desc := textarea.New()
	desc.Placeholder = "Free-text task description (optional)"
	desc.SetHeight(3)
	desc.CharLimit = 5000

	var trunk string
	var baseOpts []string
	switch {
	// Require actual branches, not just Detected: a git-spice repo with nothing
	// tracked yet (or whose `gs log short` errored) has Detected true and an
	// empty slice, and session.TrunkOf falls back to a hardcoded "main" for an
	// empty stack. That fabricates a base option — preselected, and possibly a
	// branch the repo does not have — so fall through to the plain-git branches
	// instead, which is what resolveBase offers in the same situation.
	//
	// The union with plainBranches (rather than session.BranchOptions on its
	// own) exists because afk's own session branches are never git-spice
	// -tracked: assembleSteps prefers worktrunk over the git-spice creation
	// path whenever `wt` is on $PATH, so every prior session's branch is a
	// plain, untracked git branch invisible to `gs log short`. Without the
	// union, this picker hides exactly the branches a user most wants to
	// stack on. session.MergeBaseOptions is exported so resolveBase (the CLI
	// picker) and this form collapse to one implementation and cannot drift.
	case stack.Detected && len(stack.Branches) > 0:
		trunk = session.TrunkOf(stack.Branches)
		baseOpts = session.MergeBaseOptions(stack.Branches, trunk, plainBranches)
	case plainTrunk != "":
		trunk = plainTrunk
		baseOpts = session.OrderTrunkFirst(plainBranches, trunk)
	default:
		// Trunk unresolved (the fetch has not landed, or git could not answer).
		// Offer the branches unordered rather than inventing a trunk: an
		// invented "main" is a phantom option in a master-trunk repo, and
		// submitting an empty base is already correct — session.Create resolves
		// the real trunk itself in that case.
		baseOpts = plainBranches
	}

	items := make([]list.Item, len(baseOpts))
	for i, name := range baseOpts {
		items[i] = baseItem(name)
	}

	delegate := list.NewDefaultDelegate()
	delegate.SetHeight(1)
	// No blank row between branches: the list sits inside a modal with a few
	// rows to spend, and the default delegate's spacing would halve them.
	delegate.SetSpacing(0)
	delegate.ShowDescription = false

	baseL := list.New(items, delegate, 0, 0)
	// Wraps like the session list; see newSessionList in sessionlist.go.
	baseL.InfiniteScrolling = true
	baseL.Title = ""
	baseL.SetShowTitle(false)
	baseL.SetShowStatusBar(false)
	baseL.SetShowPagination(false)
	baseL.SetShowHelp(false)
	baseL.SetFilteringEnabled(true)
	baseL.DisableQuitKeybindings()

	for i, it := range items {
		if it.(baseItem) == baseItem(trunk) {
			baseL.Select(i)
			break
		}
	}

	return newForm{
		nameInput:   nameIn,
		description: desc,
		baseList:    baseL,
		provider:    defaultProviderIndex(),
		focusIndex:  formFocusName,
	}
}

func (f *newForm) moveFocus(delta int) tea.Cmd {
	f.focusIndex = (f.focusIndex + delta + formFieldCount) % formFieldCount
	var cmds []tea.Cmd
	switch f.focusIndex {
	case formFocusName:
		cmds = append(cmds, f.nameInput.Focus())
		f.description.Blur()
	case formFocusDescription:
		f.nameInput.Blur()
		cmds = append(cmds, f.description.Focus())
	case formFocusBase, formFocusAutonomous:
		// Neither has a cursor to start: the base list takes keys without a
		// focus state, and the checkbox is a bool.
		f.nameInput.Blur()
		f.description.Blur()
	case formFocusProvider:
		// An index into provider.Names(), so no cursor either. Its own arm
		// rather than a third label on the one above: shift+tab from name
		// lands here first, and a missing arm would leave the name input
		// focused and swallowing keys while the selector shows focus.
		f.nameInput.Blur()
		f.description.Blur()
	}
	return tea.Batch(cmds...)
}

// resetFilterKeepingSelection clears the base filter and re-selects whatever
// the user had highlighted. bubbles' ResetFilter snaps the cursor to index 0, so
// narrowing to a branch and then pressing Esc for the full list would silently
// move the selection to trunk, and the session would be created off the wrong
// base with nothing on screen saying so.
func (f *newForm) resetFilterKeepingSelection() {
	var want string
	if sel, ok := f.baseList.SelectedItem().(baseItem); ok {
		want = string(sel)
	}
	f.baseList.ResetFilter()
	if want == "" {
		return
	}
	for i, it := range f.baseList.Items() {
		if bi, ok := it.(baseItem); ok && string(bi) == want {
			f.baseList.Select(i)
			return
		}
	}
}

// toggleAutonomous is the checkbox's only gesture. It is reached solely from
// formComponentKey's checkbox arm, so space keeps meaning a literal space in
// the name and description.
//
// It clears err: the one mode error the form raises (codex without
// Autonomous) is fixed by ticking this box, and leaving it on screen would
// report a conflict that no longer exists. Any other error is recomputed on
// the next submit anyway.
func (f *newForm) toggleAutonomous() {
	f.autonomous = !f.autonomous
	f.err = ""
}

// cycleProvider moves the Provider selector by delta (+1 forward, -1 back),
// wrapping. Choosing codex ticks Autonomous, because provider.CheckMode
// refuses codex without it and the dashboard form never creates scratch
// sessions; leaving the box for the user to discover would make the
// selector's only non-default choice fail on its first submit. Moving away
// from codex leaves the box as it is: it is still a valid choice for claude,
// and silently unticking it would discard something the user may have set
// deliberately.
func (f *newForm) cycleProvider(delta int) {
	names := provider.Names()
	f.provider = (f.provider + delta + len(names)) % len(names)
	if names[f.provider] == provider.CodexName {
		f.autonomous = true
	}
	f.err = ""
}

// providerName is the selector's current provider name.
func (f *newForm) providerName() string {
	return provider.Names()[f.provider]
}

func (f *newForm) validate() (name, base, description string, autonomous bool, providerName string, err error) {
	name = strings.TrimSpace(f.nameInput.Value())
	if verr := session.ValidateName(name); verr != nil {
		f.err = verr.Error()
		return "", "", "", false, "", verr
	}
	// scratch is always false: this form only creates planned sessions; the
	// scratch path is the `o` key, which has no form.
	providerName = f.providerName()
	if merr := provider.CheckMode(providerName, false, f.autonomous); merr != nil {
		f.err = merr.Error()
		return "", "", "", false, "", merr
	}
	f.err = ""

	description = f.description.Value()

	base = ""
	if sel := f.baseList.SelectedItem(); sel != nil {
		base = string(sel.(baseItem))
	}

	return name, base, description, f.autonomous, providerName, nil
}
