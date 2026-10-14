// stack.go implements the git-spice stack topology panel: a tree of tracked
// branches from `gs log short --json`, annotated with which discovered
// sessions own which branch.
//
// Two hard constraints from the task this implements:
//   - Read-only: the only git-spice invocation anywhere in this file is
//     internal/spice.LogShort (`gs log short --json`), via internal/spice's
//     existing parser (ParseLogShort) — reused as-is, never re-parsed here.
//     Nothing in this file constructs or runs a gs mutation command.
//   - Hidden, not placeholder-ed: when git-spice is not detected for the
//     repo (internal/spice.Detect), the panel renders nothing at all
//     (Render returns nil) rather than an empty box — see stackPanel.Render.
//
// The stack is one tree for the whole repo (git-spice's ref-backed state is
// shared across worktrees), so it is refreshed independently of per-worktree
// session discovery: stackCmd runs alongside loadCmd/tickCmd on every
// refresh tick (model.go's Init and the tickMsg case in Update).
package ui

import (
	"charm.land/lipgloss/v2/tree"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/spice"
)

// stackState is the repo-wide git-spice stack topology. It is stored on
// Model (the `stack` field) separately from the per-session `sessions`
// slice, since the stack is a single shared tree, not per-session data.
type stackState struct {
	// Detected is whether git-spice is initialized and a resolvable binary
	// exists for this repo (internal/spice.Detect). The stack panel is
	// hidden entirely when this is false — never an empty/placeholder panel
	// for non-gs users.
	Detected bool
	// Branches is the parsed, name-sorted `gs log short --json` stream
	// (internal/spice.LogShort already sorts it). Empty and Err empty when
	// Detected is true but the stack has no tracked branches yet.
	Branches []spice.Branch
	// Err is set when Detected is true but `gs log short --json` failed to
	// run or parse. Empty on success.
	Err string
}

// stackLoadedMsg carries the result of one stack-refresh pass (stackCmd).
type stackLoadedMsg struct {
	stack stackState
}

// stackCmd refreshes the repo's git-spice stack topology off the UI thread:
// detect, then — only if detected — run `gs log short --json` via
// internal/spice.LogShort. Fired alongside loadCmd/tickCmd so the stack
// panel refreshes on the same cadence as session discovery.
//
// At most one refresh runs at a time, and a tick that arrives while one is
// still in flight is DROPPED, not queued. `gs log short` is not a cheap read
// — it costs seconds on a large stack and fans out into a dozen concurrent
// `git` children — so on any repo where it outlasts the 2s tick, refreshing
// unconditionally is self-amplifying: overlapping invocations contend, the
// contention makes each one slower, and slower invocations overlap harder.
// It does not settle at some higher concurrency; it runs away until the
// machine is saturated. Dropping the tick degrades the panel to "as fresh as
// git-spice can actually answer", which is the honest ceiling anyway.
//
// The guard releases in the Cmd goroutine rather than on stackLoadedMsg, so
// nothing can wedge it permanently by dropping the message; internal/spice's
// timeout is what guarantees the goroutine reaches the release at all.
func (m Model) stackCmd() tea.Cmd {
	repo := m.repo
	detect := m.deps.SpiceDetect
	logShort := m.deps.SpiceLogShort
	refreshing := m.stackRefreshing
	return func() tea.Msg {
		// A nil guard is a Model built as a bare struct literal instead of by
		// New — every such Model is a test's, driving one refresh at a time.
		if refreshing != nil {
			if !refreshing.CompareAndSwap(false, true) {
				return nil
			}
			defer refreshing.Store(false)
		}
		if !detect(repo) {
			return stackLoadedMsg{stack: stackState{Detected: false}}
		}
		branches, err := logShort(repo)
		if err != nil {
			return stackLoadedMsg{stack: stackState{Detected: true, Err: err.Error()}}
		}
		return stackLoadedMsg{stack: stackState{Detected: true, Branches: branches}}
	}
}

// plainBranchesCmd fetches the plain-git base options for the creation form.
// It now runs unconditionally, in both git-spice and plain-git repos: since
// W2.T2, newNewForm's git-spice arm unions this result into the picker via
// session.MergeBaseOptions, because a session branch created through
// worktrunk is never git-spice-tracked and so is invisible to `gs log
// short` alone — the plain-git branches are what surfaces it. Paying the
// per-tick cost deliberately (rather than adding an in-flight guard like
// stackCmd's) is fine here because `git for-each-ref` and `git rev-parse`
// return in milliseconds; they cannot pile up under overlapping ticks the
// way `gs log short` did on a large stack (stackCmd's doc comment above
// explains that runaway).
//
// The old `if m.stack.Detected { return nil }` gate was already leaky before
// this task: m.stack.Detected is false at Init, so this fetch always fired
// at least once regardless of the gate, and a git-spice repo that fell
// through to the plain-trunk arm (empty tracked stack, or a `gs log short`
// error) rendered that one stale snapshot for the rest of the run — not an
// empty picker; stackLoadedMsg{Detected: true, Err} folds into the
// plainTrunk-preselected arm, not the "nothing loaded yet" default.
func (m Model) plainBranchesCmd() tea.Cmd {
	repo := m.repo
	fetch := m.deps.PlainBranches
	resolveTrunk := m.deps.PlainTrunk
	return func() tea.Msg {
		branches, err := fetch(repo)
		if err != nil {
			return plainBranchesLoadedMsg{err: err}
		}
		// A listing that worked is never nil, even with nothing to list:
		// notifyBranchesError reads nil branches as "the listing itself
		// failed". A repository with no commits yet (a folder the dashboard
		// has just made for a new plan, T7.5) lists no branches and has no
		// trunk, which is its normal state, not a failure. Left nil, it
		// raised "branch listing failed" seconds after the planning pane
		// opened, over the new plan's own notice.
		if branches == nil {
			branches = []string{}
		}
		trunk, terr := resolveTrunk(repo)
		return plainBranchesLoadedMsg{branches: branches, trunk: trunk, err: terr}
	}
}

// stackFailedStatus and branchesFailedStatus are the notification text for
// notifyStackError/notifyBranchesError (model.go). Both are outcome-only,
// with no divider and no key hint: there is no keypress that retries a
// refresh the user did not trigger in the first place, which is the case the
// house style's "no hint at all when there is no useful key" rule
// (messages.go) exists for.
func stackFailedStatus(err string) string {
	return "stack refresh failed: " + err
}

func branchesFailedStatus(err error) string {
	return "branch listing failed: " + err.Error()
}

// stackPanel renders the branch tree for the detail pane. It is the first
// panel that needs more than the selected Session (it annotates every
// branch with the session(s) that own it), which is why panelContext (views.go)
// carries the full session list and the stack state alongside the selected
// Session.
type stackPanel struct{}

func (stackPanel) Title() string { return "Stack (git-spice)" }

// Render returns nil (hidden) when git-spice is not detected — per the
// task's explicit "do not show an empty/placeholder stack panel for non-gs
// users." A detected-but-erroring or detected-but-empty stack still renders
// a line explaining that, since those are informative, not placeholders.
func (stackPanel) Render(ctx panelContext, width int) []string {
	st := ctx.Stack
	if !st.Detected {
		return nil
	}
	if st.Err != "" {
		return []string{theme.staleText.Render("gs log short --json: " + st.Err)}
	}
	if len(st.Branches) == 0 {
		return []string{theme.dim.Render("No tracked branches.")}
	}
	return renderStackTree(st.Branches, ctx.Sessions, width)
}

// owningSessions returns the comma-joined names of every discovered session
// whose status.json `branch` field equals branchName. Usually zero or one;
// more than one (two sessions pointed at the same branch) is displayed, not
// treated as an error.
//
// Worktree-side fault rows never count: their Branch comes from the worktree
// porcelain, not from any status.json, and the row itself is the verdict that
// NO session answers for that worktree — annotating the branch with one would
// label it as owned by the entry that reports its owner missing.
func owningSessions(sessions []Session, branchName string) string {
	var names []string
	for _, s := range sessions {
		if s.Branch == branchName && !isWorktreeFault(s.Kind) {
			names = append(names, s.Name)
		}
	}
	return strings.Join(names, ", ")
}

// renderStackTree renders branches as a tree with drawn connectors — lipgloss's
// tree package (lipgloss/v2/tree), one Tree per root (a branch with no Down
// parent — normally the trunk), children walked depth-first. Siblings and roots
// are sorted by name for deterministic output, since `gs log short --json`'s
// own order is documented as unspecified (internal/spice.ParseLogShort already
// sorts the flat list by name; that does not by itself order the tree, since
// Ups order comes straight from gs).
//
//	○ main
//	├── ● feat-auth [feat-auth]
//	│   ╰── ○ feat-billing [feat-billing]
//	╰── ○ fix-login-redirect
//
// Labels are styled here (renderStackLabel) and the connectors dimmed through
// the tree's own styles; each finished line is truncated to width, since the
// tree package lays out but never cuts.
func renderStackTree(branches []spice.Branch, sessions []Session, width int) []string {
	byName := make(map[string]spice.Branch, len(branches))
	for _, b := range branches {
		byName[b.Name] = b
	}

	var roots []string
	for _, b := range branches {
		if b.Down == nil || b.Down.Name == "" {
			roots = append(roots, b.Name)
		}
	}
	sort.Strings(roots)

	seen := make(map[string]bool, len(branches))
	var build func(name string) *tree.Tree
	build = func(name string) *tree.Tree {
		seen[name] = true
		b := byName[name]
		t := tree.Root(renderStackLabel(b, sessions)).
			Enumerator(stackEnumerator).
			Indenter(stackIndenter).
			EnumeratorStyle(theme.dim).
			IndenterStyle(theme.dim)
		children := make([]string, 0, len(b.Ups))
		for _, up := range b.Ups {
			// Defensive: a malformed/cyclic gs log stream must not hang the
			// renderer in an infinite walk, and a child the stream never
			// described has nothing to draw.
			if _, ok := byName[up.Name]; ok && !seen[up.Name] {
				children = append(children, up.Name)
			}
		}
		sort.Strings(children)
		for _, c := range children {
			if seen[c] {
				continue
			}
			t.Child(build(c))
		}
		return t
	}

	var out []string
	emit := func(t *tree.Tree) {
		for _, ln := range strings.Split(t.String(), "\n") {
			out = append(out, truncate(ln, width))
		}
	}
	for _, r := range roots {
		if !seen[r] {
			emit(build(r))
		}
	}
	// Any branch never reached from a root (no root found, or a disconnected
	// fragment) is still surfaced, flattened at depth 0 — read-only display
	// must never silently drop data.
	for _, b := range branches {
		if !seen[b.Name] {
			seen[b.Name] = true
			out = append(out, truncate(renderStackLabel(b, sessions), width))
		}
	}
	return out
}

// stackEnumerator and stackIndenter are the tree's connectors, four cells wide
// with a trailing space so the branch marker does not butt against the line
// glyph ("├── ● feat-auth", not "├──● feat-auth"). The package's own
// RoundedEnumerator/DefaultIndenter pair is three cells and spaceless; a
// custom pair is the supported way to change that (tree.Enumerator/Indenter).
func stackEnumerator(children tree.Children, index int) string {
	if children.Length()-1 == index {
		return "╰── "
	}
	return "├── "
}

func stackIndenter(children tree.Children, index int) string {
	if children.Length()-1 == index {
		return "    "
	}
	return "│   "
}

// renderStackLabel is one branch's node text: a marker for the current branch,
// the branch name, and — when a discovered session owns it — the owning session
// name(s) in brackets, dimmed.
func renderStackLabel(b spice.Branch, sessions []Session) string {
	marker := theme.taskPending.Render("○")
	name := b.Name
	if b.Current {
		marker = theme.taskComplete.Render("●")
		name = theme.sessionName.Render(name)
	}
	label := marker + " " + name
	if owner := owningSessions(sessions, b.Name); owner != "" {
		label += " " + theme.dim.Render("["+owner+"]")
	}
	return label
}
