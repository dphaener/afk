package masterplan

import (
	"regexp"
	"strings"
)

var (
	sectionNumberRE = regexp.MustCompile(`^\d+\.\s*`)
	separatorCellRE = regexp.MustCompile(`^:?-+:?$`)
	// taskIDTokenRE finds a task id that is not part of a longer word, so
	// "after T2.4" names T2.4 and "XT2.4" or "T2.4bc" names nothing.
	taskIDTokenRE = regexp.MustCompile(`\bT\d+\.\d+[a-z]?\b`)
	spaceRunRE    = regexp.MustCompile(`\s+`)
)

// Question is one row of a master plan's Open questions table.
//
// The checker (afk plan lint) and the daemon both read the table through
// Questions, so "open" means the same thing to both: a row is open unless its
// question cell contains "**Answered". A looser rule in one reader would let
// the checker call a plan clean while the daemon held a task for a question
// the owner had already answered, or the other way round.
type Question struct {
	// Line is the 1-based line of the row.
	Line int
	// Text is the question cell, trimmed, with "\|" read as a literal pipe.
	Text string
	// Row is the whole source line without its newline, exactly as it is in
	// the file. AnswerRow returns it as the text to replace, so it has to
	// match the file byte for byte.
	Row string
	// NeededBy is every task id in the "Needed by" cell, in order, each once.
	NeededBy []string
	// Open is false when Text contains "**Answered".
	Open bool
}

// Questions returns every row of the Open questions table, open and answered,
// in plan order. The table is the first one with a "Needed by" column under
// the "Open questions" section (a leading number in the title is ignored);
// fenced lines are skipped and the table ends at the next heading of level 2
// or above. It returns nil when the plan has no such section or table.
func Questions(data []byte) []Question {
	o := Scan(data)
	lines := strings.Split(string(data), "\n")
	raw := make([]string, len(lines))
	for i, l := range lines {
		raw[i] = l
		// Scan trims trailing white space from each line; do the same so a
		// line number here is the line number there and o.Fenced lines up.
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	fenced := func(i int) bool { return i < len(o.Fenced) && o.Fenced[i] }

	hi := sectionHeading(o.Headings, "Open questions")
	if hi < 0 {
		return nil
	}
	start, end := o.Headings[hi].Line, len(lines) // start is the 0-based line after the heading
	for _, h := range o.Headings[hi+1:] {
		if h.Level <= 2 {
			end = h.Line - 1
			break
		}
	}

	isRow := func(i int) bool {
		return !fenced(i) && strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "|")
	}
	for i := start; i < end; i++ {
		if !isRow(i) {
			continue
		}
		last := i
		for last+1 < end && isRow(last+1) {
			last++
		}
		needCol, questionCol := -1, 0
		for c, cell := range splitRow(lines[i]) {
			switch {
			case strings.EqualFold(cell, "Needed by") && needCol < 0:
				needCol = c
			case strings.EqualFold(cell, "Question"):
				questionCol = c
			}
		}
		if needCol < 0 {
			i = last
			continue
		}
		var rows []Question
		for r := i + 1; r <= last; r++ {
			cells := splitRow(lines[r])
			if isSeparator(cells) {
				continue
			}
			text := cellAt(cells, questionCol)
			rows = append(rows, Question{
				Line:     r + 1,
				Text:     text,
				Row:      strings.TrimSuffix(raw[r], "\r"),
				NeededBy: uniqueIDs(cellAt(cells, needCol)),
				Open:     !strings.Contains(text, "**Answered"),
			})
		}
		return rows
	}
	return nil
}

// AnswerRow writes an answer into the one open row whose question text is
// question. It returns that row's source line as old and, as new, the same
// line with " **Answered <date>:** <answer>" added at the end of the question
// cell's text. The question stays, and the other cells and the cell padding
// are untouched, so the edit is one small change to one line, which is what a
// guarded patch of the plan wants.
//
// The answer is cleaned before it is written: every run of white space
// (newlines included) becomes one space, because a table row is one line, and
// each "|" is written "\|", because an owner's answer is free text and a bare
// pipe would end the cell and shift "Needed by" onto the wrong text.
//
// ok is false when no open row has that text, when more than one does (the
// daemon cannot tell which one the owner meant), or when the edit would not
// read back as the same row now answered, which keeps a stray backtick in an
// answer from swallowing the pipes after it.
func AnswerRow(plan []byte, question, date, answer string) (old, new []byte, ok bool) {
	var hit *Question
	for _, q := range Questions(plan) {
		if q.Open && q.Text == question {
			if hit != nil {
				return nil, nil, false
			}
			q := q
			hit = &q
		}
	}
	if hit == nil {
		return nil, nil, false
	}
	answer = strings.TrimSpace(spaceRunRE.ReplaceAllString(answer, " "))
	answer = strings.ReplaceAll(answer, "|", `\|`)

	spans := rowSpans(hit.Row)
	// The question column is the one splitRow's Text came from. Find it by
	// matching the cell text, since the header row is not at hand here.
	col := -1
	for c, s := range spans {
		if s.text == question {
			col = c
			break
		}
	}
	if col < 0 {
		return nil, nil, false
	}
	insert := " **Answered " + date + ":** " + answer
	e := spans[col].end
	if strings.HasSuffix(answer, `\`) && strings.HasPrefix(hit.Row[e:], "|") {
		// A trailing backslash straight before the next pipe would read as
		// an escaped pipe.
		insert += " "
	}
	newRow := hit.Row[:e] + insert + hit.Row[e:]

	lines := strings.Split(string(plan), "\n")
	lines[hit.Line-1] = newRow
	patched := []byte(strings.Join(lines, "\n"))

	for _, q := range Questions(patched) {
		if q.Line == hit.Line {
			if q.Open || !sameStrings(q.NeededBy, hit.NeededBy) || len(rowSpans(newRow)) != len(spans) {
				return nil, nil, false
			}
			return []byte(hit.Row), []byte(newRow), true
		}
	}
	return nil, nil, false
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// uniqueIDs returns the task ids in s, in order, each once, or nil.
func uniqueIDs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range taskIDTokenRE.FindAllString(s, -1) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// sectionHeading returns the index in headings of the first level 2 heading
// whose title starts with name, or -1. A leading number and dot are dropped
// and case is ignored, so "## 9. Deferred / later" is the Deferred section.
func sectionHeading(headings []Heading, name string) int {
	for i, h := range headings {
		if h.Level != 2 {
			continue
		}
		text := strings.ToLower(sectionNumberRE.ReplaceAllString(h.Text, ""))
		if strings.HasPrefix(text, strings.ToLower(name)) {
			return i
		}
	}
	return -1
}

func cellAt(cells []string, i int) string {
	if i < len(cells) {
		return cells[i]
	}
	return ""
}

func isSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if !separatorCellRE.MatchString(c) {
			return false
		}
	}
	return true
}

// cellSpan is one table cell: its text as read (an escaped pipe is a pipe)
// and where its trimmed text sits in the source line, so an edit can land at
// the end of the text and leave the padding alone.
type cellSpan struct {
	text       string
	start, end int
}

// splitRow splits a table row into trimmed cells. A pipe written "\|" and a
// pipe inside a backtick code span do not end a cell: plans quote shell
// commands and patterns in their questions, and splitting on those pipes
// would shift "Needed by" onto the wrong text.
func splitRow(line string) []string {
	spans := rowSpans(line)
	cells := make([]string, len(spans))
	for i, s := range spans {
		cells[i] = s.text
	}
	return cells
}

// rowSpans is splitRow with positions. Offsets are byte offsets into line.
func rowSpans(line string) []cellSpan {
	trimmed := strings.TrimSpace(line)
	base := strings.Index(line, trimmed)
	if strings.HasPrefix(trimmed, "|") {
		base++
	}
	s := strings.TrimPrefix(trimmed, "|")
	var spans []cellSpan
	var cur strings.Builder
	cellStart := 0
	span := 0 // length of the backtick run that opened the current code span
	flush := func(at int) {
		raw := s[cellStart:at]
		lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
		text := strings.TrimSpace(cur.String())
		spans = append(spans, cellSpan{
			text:  text,
			start: base + cellStart + lead,
			end:   base + cellStart + len(strings.TrimRight(raw, " \t\r\n")),
		})
		cur.Reset()
	}
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s) && s[i+1] == '|':
			cur.WriteByte('|')
			i += 2
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			switch {
			case span == n:
				span = 0
			case span == 0 && hasBacktickRun(s[i+n:], n):
				// A run with no matching closing run is plain text, so one
				// stray backtick cannot swallow the rest of the row.
				span = n
			}
			cur.WriteString(s[i : i+n])
			i += n
		case c == '|' && span == 0:
			flush(i)
			cellStart = i + 1
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	// Text after the last pipe is a cell only if there is any; a row
	// normally ends with a pipe.
	if strings.TrimSpace(cur.String()) != "" {
		flush(len(s))
	}
	return spans
}

// hasBacktickRun reports whether s holds a run of exactly n backticks.
func hasBacktickRun(s string, n int) bool {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == n {
			return true
		}
		i = j
	}
	return false
}
