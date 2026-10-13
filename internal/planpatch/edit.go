package planpatch

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dphaener/afk/internal/masterplan"
)

// This file holds the builders: pure functions that turn "put this task
// here" into the old and new text of a guarded patch. They exist because a
// planning agent is good at writing a task and poor at counting lines and
// blank lines, and a patch placed by hand in a long plan lands in the wrong
// milestone sooner or later. So the agent writes the entry and code places
// it.
//
// A builder checks nothing that Apply checks. It does not look at the
// ledger and does not run the checker; its Edit goes through Apply like a
// patch typed by hand, so rule 8 and the checker hold for it too. A builder
// only inserts lines or rewrites one Depends on line. It never renumbers,
// moves or removes anything.

// Where says where InsertTask puts a task. After names the task the new one
// goes directly after. When After is empty, Milestone is the number of the
// milestone the new task is added to the end of. Milestone 0 is a real
// milestone (a plan may open with "### M0"), which is why an empty After,
// and not a zero Milestone, is what selects the milestone form.
type Where struct {
	After     string
	Milestone int
}

var (
	// milestoneHeadingRE reads the number from the text of a level 3
	// milestone heading. It is the shape planlint counts as a milestone
	// ("M<n>", a dash with space each side, a title), with the number
	// captured.
	milestoneHeadingRE = regexp.MustCompile(`^M(\d+)\s+[—–-]\s+\S`)
	// dependsLineRE splits a Depends on line into its label, the space
	// after it and its value. masterplan has the same pattern unexported;
	// AddDependency does not trust this copy and reads the patched plan
	// back through masterplan.Scan before it returns.
	dependsLineRE = regexp.MustCompile(`^(\s*-\s+\*\*Depends on:\*\*)(\s*)(.*)$`)
	// dependsIDRE is a task id at the start of one comma-separated part of
	// a Depends on value, as masterplan reads one.
	dependsIDRE = regexp.MustCompile(`^T\d+\.\d+[a-z]?(\s|$)`)
)

// doc is a plan with what the builders need to find a place in it: its
// lines, the byte offset each line starts at, and masterplan's scan.
type doc struct {
	text  []byte
	lines []string
	// starts[i] is the byte offset of line i+1. Lines are split on "\n"
	// alone, as masterplan.Scan splits them, so a line number from the scan
	// is an index here.
	starts []int
	o      masterplan.Outline
}

func read(plan []byte) doc {
	d := doc{text: plan, lines: strings.Split(string(plan), "\n"), o: masterplan.Scan(plan)}
	d.starts = make([]int, len(d.lines))
	off := 0
	for i, l := range d.lines {
		d.starts[i] = off
		off += len(l) + 1
	}
	return d
}

func (d doc) task(id string) (masterplan.Task, bool) {
	for _, t := range d.o.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return masterplan.Task{}, false
}

// milestoneNumber returns the number of a milestone heading, or false when
// the heading is not one.
func milestoneNumber(h masterplan.Heading) (int, bool) {
	if h.Level != 3 {
		return 0, false
	}
	m := milestoneHeadingRE.FindStringSubmatch(h.Text)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// milestone returns the first heading of milestone n.
func (d doc) milestone(n int) (masterplan.Heading, bool) {
	for _, h := range d.o.Headings {
		if got, ok := milestoneNumber(h); ok && got == n {
			return h, true
		}
	}
	return masterplan.Heading{}, false
}

// sectionEnd returns the last line (1-based) of the section that starts at
// the heading on line: the line before the next heading of level maxLevel or
// higher up, or the last line of the file.
func (d doc) sectionEnd(line, maxLevel int) int {
	for _, h := range d.o.Headings {
		if h.Line > line && h.Level <= maxLevel {
			return h.Line - 1
		}
	}
	return len(d.lines)
}

// lastText returns the last line in from..to (1-based, inclusive) that is
// not blank. from is a heading line, so there always is one.
func (d doc) lastText(from, to int) int {
	for n := to; n > from; n-- {
		if strings.TrimSpace(d.lines[n-1]) != "" {
			return n
		}
	}
	return from
}

// insertAfter returns the Edit that puts block after line n (1-based), with
// one blank line before it and one after it when more text follows.
//
// It only ever adds lines. A blank line the plan already has after line n is
// left where it is and serves as the blank line after the block, so the
// lines of the task above are not rewritten. That matters for rule 8: the
// task above may be running, and Apply reads a removed line of its entry as
// a change to it, while new text that opens with a heading and sits after
// the last line of its entry is not.
func (d doc) insertAfter(n int, block []byte) Edit {
	var ins []byte
	pos := len(d.text)
	if n < len(d.lines) {
		// Line n ends with a line break, so one more makes the blank line.
		pos = d.starts[n]
		ins = append(ins, '\n')
	} else {
		// Line n is the last line and the file does not end with a line
		// break: end the line first.
		ins = append(ins, '\n', '\n')
	}
	ins = append(ins, block...)
	if n < len(d.lines) && strings.TrimSpace(d.lines[n]) != "" {
		ins = append(ins, '\n')
	}
	return d.anchored(pos, pos, ins)
}

// anchored returns the Edit that replaces bytes a to b of the plan with
// repl, with enough of the plan in Old that it is there exactly once.
//
// The bytes to replace are rarely unique on their own (an insertion has
// none, and "- **Depends on:** T1.1" is under many tasks), so Old starts at
// the nearest heading line above the place. A task heading carries its id
// and a milestone heading its number, so that is nearly always enough. When
// it is not (the same heading is also shown in a code fence, say), Old
// starts one heading higher, and so on up to the first line; the whole file
// is the last resort and is always there once.
//
// bytes.Count does not count overlapping matches, so the first match is also
// checked to be the one meant: bytes.Replace changes the first.
func (d doc) anchored(a, b int, repl []byte) Edit {
	build := func(from, to int) Edit {
		e := Edit{Old: append([]byte{}, d.text[from:to]...)}
		e.New = append(e.New, d.text[from:a]...)
		e.New = append(e.New, repl...)
		e.New = append(e.New, d.text[b:to]...)
		return e
	}
	try := func(from int) (Edit, bool) {
		old := d.text[from:b]
		if len(old) == 0 || bytes.Count(d.text, old) != 1 || bytes.Index(d.text, old) != from {
			return Edit{}, false
		}
		return build(from, b), true
	}
	for i := len(d.o.Headings) - 1; i >= 0; i-- {
		from := d.starts[d.o.Headings[i].Line-1]
		if from > a {
			continue
		}
		if e, ok := try(from); ok {
			return e
		}
	}
	if e, ok := try(0); ok {
		return e
	}
	return build(0, len(d.text))
}

// patched returns the plan with e applied, for a builder to read back.
func (d doc) patched(e Edit) doc {
	return read(bytes.Replace(d.text, e.Old, e.New, 1))
}

// trimBlock drops blank lines from the start and the end of text and ends
// it with one line break, so the blank lines around an inserted block are
// the builder's and do not depend on how the entry file was saved.
func trimBlock(text []byte) []byte {
	lines := strings.Split(string(text), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// InsertTask returns the Edit that adds one task entry to the plan: directly
// after the entry of the task w.After, or at the end of milestone
// w.Milestone when w.After is empty.
//
// entry is the task as it should stand in the plan: its heading and the
// lines under it. Its id must be new to the plan. The builder does not pick
// or change the id (NextInMilestone and Between pick one) and does not check
// that the id's number fits the place.
func InsertTask(plan, entry []byte, w Where) (Edit, error) {
	d := read(plan)
	block := trimBlock(entry)
	if block == nil {
		return Edit{}, fmt.Errorf("the entry is empty. Write the task in it, starting with a heading like \"#### T2.3 — Title\"")
	}
	bo := masterplan.Scan(block)
	if len(bo.Tasks) == 0 || bo.Tasks[0].Line != 1 {
		return Edit{}, fmt.Errorf("the entry must start with a task heading like \"#### T2.3 — Title\" (four hashes, a capital T, a dash with a space each side, then a title). It starts with %q", firstLine(block))
	}
	if len(bo.Headings) != 1 {
		return Edit{}, fmt.Errorf("the entry holds more than one heading (%q). Put one task in an entry, its heading and the lines under it, and add each further task with an insert of its own", bo.Headings[1].Text)
	}
	id := bo.Tasks[0].ID
	if _, ok := d.task(id); ok {
		return Edit{}, fmt.Errorf("task %s is already in the plan, so the entry cannot use that id. Ask for a free one with afk plan next-id and put it in the entry's heading", id)
	}

	var last int
	if w.After != "" {
		if w.Milestone != 0 {
			return Edit{}, fmt.Errorf("the new task was given both a task to go after (%s) and a milestone (M%d). Name one of the two", w.After, w.Milestone)
		}
		t, ok := d.task(w.After)
		if !ok {
			return Edit{}, fmt.Errorf("there is no task %s in the plan, so the new task cannot go after it. Run afk tasks to see the ids", w.After)
		}
		last = d.lastText(t.Line, d.sectionEnd(t.Line, 4))
	} else {
		h, ok := d.milestone(w.Milestone)
		if !ok {
			return Edit{}, fmt.Errorf("the plan has no milestone M%d (a heading like \"### M%d — Title\"), so the new task cannot go into it. Name a milestone the plan has, or add a new one", w.Milestone, w.Milestone)
		}
		last = d.lastText(h.Line, d.sectionEnd(h.Line, 3))
	}

	e := d.insertAfter(last, block)
	// Read the result back as the daemon would. A code fence that is never
	// closed above the place hides everything after it, the new heading
	// too: the patch would add no error and no task, and the owner would be
	// told a task was added that the daemon cannot see.
	after := d.patched(e)
	if _, ok := after.task(id); !ok || len(after.o.Tasks) != len(d.o.Tasks)+1 {
		return Edit{}, fmt.Errorf("task %s would not be read as a task at that place. Look for a code fence (``` or ~~~) above it that is never closed, close it with afk plan patch, then try again", id)
	}
	return e, nil
}

// AppendMilestone returns the Edit that adds a whole milestone after the
// plan's last one: after the end of the last milestone's section, which is
// the line before the next heading of level 1 or 2, or the end of the file.
//
// block is the milestone as it should stand in the plan: its "### M<n> —
// Title" heading, then its text and its tasks. Its number and its task ids
// must be new to the plan.
func AppendMilestone(plan, block []byte) (Edit, error) {
	d := read(plan)
	b := trimBlock(block)
	if b == nil {
		return Edit{}, fmt.Errorf("the milestone is empty. Write it starting with a heading like \"### M3 — Title\"")
	}
	bo := masterplan.Scan(b)
	n, ok := 0, false
	if len(bo.Headings) > 0 && bo.Headings[0].Line == 1 {
		n, ok = milestoneNumber(bo.Headings[0])
	}
	if !ok {
		return Edit{}, fmt.Errorf("the milestone must start with a heading like \"### M3 — Title\" (three hashes, a capital M and its number, a dash with a space each side, then a title). It starts with %q", firstLine(b))
	}
	for _, h := range bo.Headings[1:] {
		if h.Level < 4 {
			return Edit{}, fmt.Errorf("the milestone holds a second section heading (%q). Put one milestone in it: one \"### M<n> — Title\" heading and the tasks under it", h.Text)
		}
	}
	if h, used := d.milestone(n); used {
		return Edit{}, fmt.Errorf("the plan already has a milestone M%d (%q). Give the new milestone the next number that is free", n, h.Text)
	}
	for _, t := range bo.Tasks {
		if _, used := d.task(t.ID); used {
			return Edit{}, fmt.Errorf("task %s is already in the plan, so the new milestone cannot use that id. Number its tasks T%d.1, T%d.2 and so on", t.ID, n, n)
		}
	}

	lastMilestone := -1
	for _, h := range d.o.Headings {
		if _, ok := milestoneNumber(h); ok {
			lastMilestone = h.Line
		}
	}
	if lastMilestone < 0 {
		return Edit{}, fmt.Errorf("the plan has no milestone yet (a heading like \"### M1 — Title\"), so afk cannot tell where a new one goes. Add the first milestone with afk plan patch")
	}
	last := d.lastText(lastMilestone, d.sectionEnd(lastMilestone, 2))

	e := d.insertAfter(last, b)
	// The same read-back as InsertTask, for the same reason.
	after := d.patched(e)
	if _, ok := after.milestone(n); !ok || len(after.o.Tasks) != len(d.o.Tasks)+len(bo.Tasks) {
		return Edit{}, fmt.Errorf("milestone M%d would not be read as a milestone at that place. Look for a code fence (``` or ~~~) above it that is never closed, close it with afk plan patch, then try again", n)
	}
	return e, nil
}

// AddDependency returns the Edit that makes task wait for on, by rewriting
// the task's Depends on line: a line that says none ("—", "-", "None")
// becomes on, and a line of ids gets ", on" after its last id, with any
// text after the ids kept.
//
// It returns ErrNoChange when task already waits for on. A line it cannot
// add to safely (prose with no id, a note in brackets that runs on to the
// next line) is an error that says to patch the line by hand: guessing
// there could change what a task waits for, which is the one thing the
// daemon acts on.
func AddDependency(plan []byte, task, on string) (Edit, error) {
	d := read(plan)
	t, ok := d.task(task)
	if !ok {
		return Edit{}, fmt.Errorf("there is no task %s in the plan. Run afk tasks to see the ids", task)
	}
	if _, ok := d.task(on); !ok {
		return Edit{}, fmt.Errorf("there is no task %s in the plan, so %s cannot wait for it. Add the task first, or check the id with afk tasks", on, task)
	}
	if task == on {
		return Edit{}, fmt.Errorf("a task cannot wait for itself (%s). Name the task it must wait for", task)
	}
	for _, dep := range t.DependsOn {
		if dep == on {
			return Edit{}, ErrNoChange
		}
	}

	// The task's Depends on line is the first one in its entry outside a
	// code fence, which is the line masterplan read.
	line := 0
	var m []string
	for n := t.Line + 1; n <= d.sectionEnd(t.Line, 4); n++ {
		if d.o.Fenced[n-1] {
			continue
		}
		if m = dependsLineRE.FindStringSubmatch(strings.TrimRight(d.lines[n-1], " \t\r")); m != nil {
			line = n
			break
		}
	}
	if line == 0 {
		return Edit{}, fmt.Errorf("task %s has no Depends on line to add %s to. Add \"- **Depends on:** %s\" under its heading with afk plan patch", task, on, on)
	}
	value, err := addToDepends(m[3], on)
	if err != nil {
		return Edit{}, fmt.Errorf("the Depends on line of %s says %q, %w. Change that line with afk plan patch", task, m[3], err)
	}
	space := m[2]
	if space == "" {
		space = " "
	}
	raw := d.lines[line-1]
	trimmed := strings.TrimRight(raw, " \t\r")
	// What was trimmed off the end (a "\r" in a file with Windows line
	// endings) goes back on, so the rest of the line is byte for byte as it
	// was.
	newLine := m[1] + space + value + raw[len(trimmed):]

	start := d.starts[line-1]
	e := d.anchored(start, start+len(raw), []byte(newLine))
	// Read the result back through masterplan, whose reading is the one the
	// daemon uses. The line was rewritten with this file's own copy of the
	// pattern, and the only safe outcome is: the same dependencies as
	// before, plus on.
	got, _ := d.patched(e).task(task)
	want := append(append([]string{}, t.DependsOn...), on)
	if strings.Join(got.DependsOn, ",") != strings.Join(want, ",") {
		return Edit{}, fmt.Errorf("the Depends on line of %s says %q, which afk cannot add %s to without changing what the task waits for. Change that line with afk plan patch", task, m[3], on)
	}
	return e, nil
}

// addToDepends returns the value of a Depends on line with on added. The
// error is the middle of a sentence; AddDependency puts the task and the
// advice around it.
//
// It follows masterplan's reading of the value: text in brackets is a note
// and is passed over, the value is split at commas, and the ids are the
// parts that start with one, up to the first part that does not. The new id
// goes after the last of those ids, and after a note in brackets that
// directly follows it: in "T1.1 (reuses its page)" the note is about T1.1
// and must not end up after the new id.
func addToDepends(value, on string) (string, error) {
	switch v := strings.TrimSpace(value); {
	case v == "", v == "—", v == "–", v == "-", strings.EqualFold(v, "none"):
		return on, nil
	}

	// stripped is the value without its bracketed notes, and at[i] is where
	// byte i of stripped sits in value.
	var stripped []byte
	var at []int
	depth := 0
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case depth == 0:
			stripped = append(stripped, c)
			at = append(at, i)
		}
	}

	end := -1 // offset in stripped just after the last id
	off := 0
	for _, part := range strings.Split(string(stripped), ",") {
		lead := len(part) - len(strings.TrimLeft(part, " \t"))
		id := dependsIDRE.FindString(strings.TrimSpace(part))
		if id == "" {
			break
		}
		end = off + lead + len(strings.TrimSpace(id))
		off += len(part) + 1
	}
	if end < 0 {
		return "", fmt.Errorf("which names no task id")
	}
	pos := at[end-1] + 1

	// Step over notes in brackets that directly follow the last id.
	for {
		i := pos
		for i < len(value) && (value[i] == ' ' || value[i] == '\t') {
			i++
		}
		if i >= len(value) || value[i] != '(' {
			break
		}
		depth, closed := 0, -1
		for j := i; j < len(value) && closed < 0; j++ {
			switch value[j] {
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					closed = j
				}
			}
		}
		if closed < 0 {
			return "", fmt.Errorf("with a note in brackets that is not closed on that line")
		}
		pos = closed + 1
	}
	return value[:pos] + ", " + on + value[pos:], nil
}

func firstLine(text []byte) string {
	line, _, _ := strings.Cut(string(text), "\n")
	return strings.TrimRight(line, "\r")
}
