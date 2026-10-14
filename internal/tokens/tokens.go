// Package tokens turns per-call usage records into the token ledger of
// docs/PLAN.md §4.7: one JSON line per task, session, phase, role and model
// in .afk/log/tokens.jsonl. It aggregates, appends, reads and renders. It does
// not read transcripts or status.json (the provider does that) and it names
// no provider.
//
// The ledger is append-only and shared by every session of a project, so an
// append takes an exclusive flock, the same way internal/plancheck does for
// the plan file. Readers take no lock: a line is written with one write call
// under the lock, and a reader that meets a half-written last line skips it
// like any other unparseable line.
package tokens

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

// Weighted is the cost proxy of §4.7:
// input + 5×output + 1.25×cache_write + 0.1×cache_read.
//
// It is a proxy built from the ratios between API prices, not a bill: the
// plan's own price list can change and a subscription is not metered per
// token. The raw fields are kept in every ledger line so the weights can be
// changed later and the ledger recomputed.
func Weighted(input, output, cacheWrite, cacheRead int64) float64 {
	return float64(input) + 5*float64(output) + 1.25*float64(cacheWrite) + 0.1*float64(cacheRead)
}

// Line is one ledger line. The JSON keys are the contract of §4.7.
type Line struct {
	At         string  `json:"at"`
	Task       string  `json:"task"`
	Session    string  `json:"session"`
	Phase      string  `json:"phase"`
	Role       string  `json:"role"`
	Model      string  `json:"model"`
	Calls      int64   `json:"calls"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheWrite int64   `json:"cache_write"`
	CacheRead  int64   `json:"cache_read"`
	Weighted   float64 `json:"weighted"`
}

// phaseRank orders the known phases; anything else sorts after them,
// alphabetically.
var phaseRank = map[string]int{
	provider.PhasePlan:    0,
	provider.PhaseExecute: 1,
	provider.PhaseReview:  2,
	provider.PhaseFix:     3,
	provider.PhaseShip:    4,
}

func phaseLess(a, b string) bool {
	ra, aok := phaseRank[a]
	rb, bok := phaseRank[b]
	switch {
	case aok && bok:
		return ra < rb
	case aok != bok:
		return aok
	}
	return a < b
}

// Aggregate sums records into one line per phase, role and model (a role can
// run on two models after a tier escalation, and a line has one model field).
// Every line is stamped with now in UTC. The order is phase, then role, then
// model, so the same records always give the same lines.
func Aggregate(records []provider.TokenRecord, task, session string, now time.Time) []Line {
	type key struct{ phase, role, model string }
	sums := map[key]*Line{}
	for _, r := range records {
		k := key{r.Phase, r.Role, r.Model}
		l := sums[k]
		if l == nil {
			l = &Line{Task: task, Session: session, Phase: r.Phase, Role: r.Role, Model: r.Model}
			sums[k] = l
		}
		l.Calls++
		l.Input += r.Input
		l.Output += r.Output
		l.CacheWrite += r.CacheWrite
		l.CacheRead += r.CacheRead
	}
	at := now.UTC().Format(time.RFC3339)
	out := make([]Line, 0, len(sums))
	for _, l := range sums {
		l.At = at
		l.Weighted = Weighted(l.Input, l.Output, l.CacheWrite, l.CacheRead)
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Phase != b.Phase {
			return phaseLess(a.Phase, b.Phase)
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Model < b.Model
	})
	return out
}

// LedgerPath is where the ledger lives in a project's main checkout.
func LedgerPath(mainCheckout string) string {
	return filepath.Join(mainCheckout, ".afk", "log", "tokens.jsonl")
}

// Append adds one JSON object per line to the ledger, creating the folder and
// the file as needed. Appending nothing is a no-op and creates nothing.
//
// The lock is a separate file beside the ledger, never the ledger itself, so
// that the ledger holds only data lines. It is left in place afterwards:
// removing it would let a writer already blocked on it lock a deleted inode
// while the next writer locks a fresh file, and the two would run at once.
// All lines go out in a single write call, so a crash cannot leave the
// ledger with some of a session's lines and not others except at a torn tail
// that Read skips.
func Append(path string, lines []Line) error {
	if len(lines) == 0 {
		return nil
	}
	var buf []byte
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			return fmt.Errorf("encoding ledger line for %s: %w", path, err)
		}
		buf = append(append(buf, b...), '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating ledger folder for %s: %w", path, err)
	}
	lp := path + ".lock"
	lock, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening ledger lock %s: %w", lp, err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", lp, err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening ledger %s: %w", path, err)
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return fmt.Errorf("writing ledger %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing ledger %s: %w", path, err)
	}
	return nil
}

// Read returns every parseable line of the ledger. A missing file is an empty
// ledger, not an error. An unparseable line (a torn write, a hand edit) is
// skipped so one bad line cannot hide the rest.
func Read(path string) ([]Line, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading ledger %s: %w", path, err)
	}
	var out []Line
	for _, raw := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var l Line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// HasSession reports whether any line belongs to the session. It is what
// makes recording a session idempotent.
func HasSession(lines []Line, session string) bool {
	for _, l := range lines {
		if l.Session == session {
			return true
		}
	}
	return false
}

// ForTask returns the lines of one task. Task ids are matched without regard
// to letter case, because people type t1.4 and plans say T1.4.
func ForTask(lines []Line, task string) []Line {
	var out []Line
	for _, l := range lines {
		if strings.EqualFold(l.Task, task) {
			out = append(out, l)
		}
	}
	return out
}

var taskIDRe = regexp.MustCompile(`T\d+\.\d+[a-z]?`)

// TaskID is the first task id (T<n>.<n>, optionally followed by one
// lower-case letter) in a session description, or "" when there is none.
func TaskID(description string) string {
	return taskIDRe.FindString(description)
}

func pct(part, total float64) string {
	if total <= 0 {
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", part/total*100)
}

func whole(v float64) string { return fmt.Sprintf("%.0f", v) }

// tableRow is one printed row of the per-task table, before it is formatted:
// a ledger line, a per-phase subtotal or the total. Both renderers print the
// same rows so the compact table can never disagree with the full one.
type tableRow struct {
	phase, role, model     string
	calls, in, out, cw, cr int64
	weighted               float64
}

// tableRows aggregates lines into the rows RenderTable prints: each line, then
// one subtotal row per phase, then the total. The returned float is the total
// weighted cost, the denominator of every share.
func tableRows(lines []Line) ([]tableRow, float64) {
	var total float64
	for _, l := range lines {
		total += l.Weighted
	}
	type sum struct {
		calls, in, out, cw, cr int64
		weighted               float64
	}
	sums := map[string]*sum{}
	var phases []string
	var all sum
	var rows []tableRow
	for _, l := range lines {
		rows = append(rows, tableRow{l.Phase, l.Role, l.Model, l.Calls, l.Input, l.Output, l.CacheWrite, l.CacheRead, l.Weighted})
		s := sums[l.Phase]
		if s == nil {
			s = &sum{}
			sums[l.Phase] = s
			phases = append(phases, l.Phase)
		}
		for _, t := range []*sum{s, &all} {
			t.calls += l.Calls
			t.in += l.Input
			t.out += l.Output
			t.cw += l.CacheWrite
			t.cr += l.CacheRead
			t.weighted += l.Weighted
		}
	}
	sort.Slice(phases, func(i, j int) bool { return phaseLess(phases[i], phases[j]) })
	for _, p := range phases {
		s := sums[p]
		rows = append(rows, tableRow{p, "(subtotal)", "", s.calls, s.in, s.out, s.cw, s.cr, s.weighted})
	}
	rows = append(rows, tableRow{"total", "", "", all.calls, all.in, all.out, all.cw, all.cr, all.weighted})
	return rows, total
}

// RenderTable prints the lines grouped as the ledger holds them, then one
// subtotal row per phase and a total row. Share is a row's weighted cost over
// the total. Numbers are right-aligned.
func RenderTable(w io.Writer, lines []Line) error {
	rows, total := tableRows(lines)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "phase\trole\tmodel\tcalls\tinput\toutput\tcache write\tcache read\tweighted\tshare\t")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t\n",
			r.phase, r.role, r.model, r.calls, r.in, r.out, r.cw, r.cr,
			whole(r.weighted), pct(r.weighted, total))
	}
	return tw.Flush()
}

// RenderCompact prints the same rows as RenderTable with only phase, role,
// model, calls, weighted and share. The dashboard uses it when its box is too
// narrow for the full table, so the words and units stay the command's own
// without horizontal scrolling.
func RenderCompact(w io.Writer, lines []Line) error {
	rows, total := tableRows(lines)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "phase\trole\tmodel\tcalls\tweighted\tshare\t")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t\n",
			r.phase, r.role, r.model, r.calls, whole(r.weighted), pct(r.weighted, total))
	}
	return tw.Flush()
}

// Short abbreviates a weighted total for a dashboard row, where every column
// counts: a whole number below 1000, then one decimal with k below a million
// and M above. The decimal is kept even when it is .0 so the width and style
// stay uniform. The unit is chosen after rounding, so 999950 reads 1.0M and
// never 1000.0k.
func Short(weighted float64) string {
	if math.Round(weighted) < 1000 {
		return whole(weighted)
	}
	if k := math.Round(weighted/100) / 10; k < 1000 {
		return fmt.Sprintf("%.1fk", k)
	}
	return fmt.Sprintf("%.1fM", weighted/1e6)
}

// StartUpRow is the start-up cost of one role: the first model call of each of
// its agents.
type StartUpRow struct {
	Role string
	// Agents is the number of agents of the role, which is the number of
	// first calls.
	Agents int64
	// AvgTokens is the average start-up size per agent: input + cache write +
	// cache read of the first call, rounded to a whole token.
	AvgTokens int64
	// Tokens is the sum behind AvgTokens, kept so the total row can average
	// across roles without rounding twice.
	Tokens int64
	// Weighted is the weighted cost of those first calls.
	Weighted float64
}

// StartUp returns one row per role from the records marked First. The first
// call of an agent carries its whole start-up context, so its input side is
// the start-up size: a fresh agent has nothing in its conversation yet, and
// whatever it sends is the system prompt, tools, skills list and CLAUDE.md.
// Output is left out of the size but not out of the weighted cost, which is
// the existing Weighted of the whole call.
//
// Rows follow the table's order: the earliest phase a role appears in, then
// the role name. A role is placed by the phase of its first-call records only,
// because those are the records this function sees.
func StartUp(records []provider.TokenRecord) []StartUpRow {
	type acc struct {
		row   StartUpRow
		phase string
	}
	byRole := map[string]*acc{}
	var order []string
	for _, r := range records {
		if !r.First {
			continue
		}
		a := byRole[r.Role]
		if a == nil {
			a = &acc{row: StartUpRow{Role: r.Role}, phase: r.Phase}
			byRole[r.Role] = a
			order = append(order, r.Role)
		}
		if phaseLess(r.Phase, a.phase) {
			a.phase = r.Phase
		}
		a.row.Agents++
		a.row.Tokens += r.Input + r.CacheWrite + r.CacheRead
		a.row.Weighted += Weighted(r.Input, r.Output, r.CacheWrite, r.CacheRead)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := byRole[order[i]], byRole[order[j]]
		if a.phase != b.phase {
			return phaseLess(a.phase, b.phase)
		}
		return order[i] < order[j]
	})
	rows := make([]StartUpRow, 0, len(order))
	for _, role := range order {
		row := byRole[role].row
		row.AvgTokens = avgTokens(row.Tokens, row.Agents)
		rows = append(rows, row)
	}
	return rows
}

func avgTokens(sum, n int64) int64 {
	if n <= 0 {
		return 0
	}
	return int64(float64(sum)/float64(n) + 0.5)
}

// RenderStartUp prints the start-up section: a heading, one row per role and a
// total row whose share is the start-up part of the whole session
// (totalWeighted is the weighted total of the session's table). With no rows
// it prints nothing, so a session read from a transcript without first-call
// marks shows no empty heading.
func RenderStartUp(w io.Writer, rows []StartUpRow, totalWeighted float64) error {
	if len(rows) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "start-up (the first call of each agent)"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "role\tagents\taverage tokens\tweighted\tshare\t")
	var agents, tokens int64
	var weighted float64
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t\n", r.Role, r.Agents, r.AvgTokens, whole(r.Weighted), pct(r.Weighted, totalWeighted))
		agents += r.Agents
		tokens += r.Tokens
		weighted += r.Weighted
	}
	fmt.Fprintf(tw, "total\t%d\t%d\t%s\t%s\t\n", agents, avgTokens(tokens, agents), whole(weighted), pct(weighted, totalWeighted))
	return tw.Flush()
}

// RenderTasks prints one row per task: the number of distinct sessions, calls,
// weighted cost and share of the ledger's total. Tasks are listed in order of
// first appearance; a line with no task id is listed as "(none)".
func RenderTasks(w io.Writer, lines []Line) error {
	type row struct {
		sessions map[string]bool
		calls    int64
		weighted float64
	}
	rows := map[string]*row{}
	var order []string
	var total float64
	for _, l := range lines {
		name := l.Task
		if name == "" {
			name = "(none)"
		}
		r := rows[name]
		if r == nil {
			r = &row{sessions: map[string]bool{}}
			rows[name] = r
			order = append(order, name)
		}
		r.sessions[l.Session] = true
		r.calls += l.Calls
		r.weighted += l.Weighted
		total += l.Weighted
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "task\tsessions\tcalls\tweighted\tshare\t")
	for _, name := range order {
		r := rows[name]
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t\n", name, len(r.sessions), r.calls, whole(r.weighted), pct(r.weighted, total))
	}
	return tw.Flush()
}
