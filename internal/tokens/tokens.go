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

// RenderTable prints the lines grouped as the ledger holds them, then one
// subtotal row per phase and a total row. Share is a row's weighted cost over
// the total. Numbers are right-aligned.
func RenderTable(w io.Writer, lines []Line) error {
	var total float64
	for _, l := range lines {
		total += l.Weighted
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "phase\trole\tmodel\tcalls\tinput\toutput\tcache write\tcache read\tweighted\tshare\t")

	type sum struct {
		calls, in, out, cw, cr int64
		weighted               float64
	}
	sums := map[string]*sum{}
	var phases []string
	var all sum
	for _, l := range lines {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t\n",
			l.Phase, l.Role, l.Model, l.Calls, l.Input, l.Output, l.CacheWrite, l.CacheRead,
			whole(l.Weighted), pct(l.Weighted, total))
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
		fmt.Fprintf(tw, "%s\t(subtotal)\t\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t\n",
			p, s.calls, s.in, s.out, s.cw, s.cr, whole(s.weighted), pct(s.weighted, total))
	}
	fmt.Fprintf(tw, "total\t\t\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t\n",
		all.calls, all.in, all.out, all.cw, all.cr, whole(all.weighted), pct(all.weighted, total))
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
