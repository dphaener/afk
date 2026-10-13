package web

import (
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/dphaener/afk/internal/masterplan"
)

// renderPlan turns a master plan's markdown into the HTML of the plan view.
//
// Its result is the one value in this package marked as already safe
// (template.HTML), so html/template writes it into the page as it is. That
// is only sound because of one rule, which every function in this file
// keeps: a tag is written by this code from a fixed string, and every piece
// of the plan's own text goes through html.EscapeString before it is joined
// to one. So nothing the plan says can open a tag, close one, or add an
// attribute. A plan is written by an agent from an interview and from files
// in the project, and it may quote anything, so its text is treated as a
// stranger's.
//
// The three places a value from the plan ends up inside a tag, and why each
// is safe:
//
//   - a heading's id is a task id that masterplan.ValidID accepted (T, digits,
//     a dot, digits, an optional letter) or a slug of a-z, 0-9 and "-";
//   - a numbered list's start is digits that strconv parsed;
//   - a link's href is text that url.Parse read as http or https with a
//     host, escaped like all other text. No other scheme becomes a link, so
//     "javascript:" is shown as the words it is.
//
// Raw HTML in the plan is not passed through: it is shown as text.
//
// The renderer covers the part of markdown a master plan uses and nothing
// more: headings, paragraphs, lists with one level of nesting, fenced code, pipe tables, block quotes,
// rules, bold, code spans and bare links. Anything else is shown as written,
// which is always readable. It is our own because the module has no markdown
// library and a plan needs so little of one.
func renderPlan(text string) template.HTML {
	r := &planRenderer{ids: map[string]int{}}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}
	r.blocks(lines, 0)
	return template.HTML(r.b.String())
}

// planRenderer holds the output and the heading ids already given out, so a
// second heading with the same words gets its own id.
type planRenderer struct {
	b   strings.Builder
	ids map[string]int
}

// maxQuoteDepth is how many block quotes inside each other are drawn as
// quotes. Deeper ones are shown as paragraphs that start with ">". Without a
// limit a line of ten thousand ">" would recurse that deep.
const maxQuoteDepth = 4

var (
	// mdHeadingRE is the same test masterplan uses for a heading line: one
	// to six "#", white space, then text. No indent, so an indented "# x"
	// inside a list stays text.
	mdHeadingRE = regexp.MustCompile(`^(#{1,6})[ \t]+(\S.*)$`)
	// mdRuleRE is a line of three or more "-", "*" or "_" and nothing else.
	mdRuleRE = regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	// mdItemRE is a list item: indent, a bullet or a number, white space,
	// text.
	mdItemRE = regexp.MustCompile(`^([ \t]*)([-*+]|\d{1,9}[.)])[ \t]+(\S.*)$`)
	// mdSeparatorRE is one cell of the line under a table's header row.
	mdSeparatorRE = regexp.MustCompile(`^:?-+:?$`)
)

// isFence uses masterplan.Scan's rule (a line whose first non-blank
// characters are ``` or ~~~ opens or closes a fence, whatever follows), so
// the page and the checker agree on which lines are code.
func isFence(line string) bool {
	lead := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(lead, "```") || strings.HasPrefix(lead, "~~~")
}

func isRule(line string) bool {
	return mdRuleRE.MatchString(strings.TrimSpace(line))
}

func isQuote(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), ">")
}

// isTableStart reports whether lines[i] is a table's header row: a line that
// starts with "|" followed by a separator line. A lone pipe line is a
// paragraph.
func isTableStart(lines []string, i int) bool {
	if !strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "|") || i+1 >= len(lines) {
		return false
	}
	cells := splitCells(lines[i+1])
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if !mdSeparatorRE.MatchString(c) {
			return false
		}
	}
	return true
}

// startsBlock reports whether lines[i] begins something other than a
// paragraph, which is where a paragraph or a list item's text ends.
func startsBlock(lines []string, i int) bool {
	l := lines[i]
	return isFence(l) || mdHeadingRE.MatchString(l) || isRule(l) || isQuote(l) || mdItemRE.MatchString(l) || isTableStart(lines, i)
}

// blocks renders lines as a run of blocks. depth counts the block quotes
// around them.
func (r *planRenderer) blocks(lines []string, depth int) {
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case strings.TrimSpace(line) == "":
			i++
		case isFence(line):
			i = r.fence(lines, i)
		case mdHeadingRE.MatchString(line):
			r.heading(line)
			i++
		case isRule(line):
			r.b.WriteString("<hr>\n")
			i++
		case isQuote(line) && depth < maxQuoteDepth:
			i = r.quote(lines, i, depth)
		case isTableStart(lines, i):
			i = r.table(lines, i)
		case mdItemRE.MatchString(line):
			i = r.list(lines, i)
		default:
			i = r.paragraph(lines, i, depth)
		}
	}
}

// fence writes a fenced block exactly as it is in the file, escaped. An
// unclosed fence runs to the end, as it does for masterplan.Scan.
func (r *planRenderer) fence(lines []string, i int) int {
	i++ // the opening line; its language word is not shown
	r.b.WriteString("<pre><code>")
	for ; i < len(lines); i++ {
		if isFence(lines[i]) {
			i++
			break
		}
		r.b.WriteString(html.EscapeString(lines[i]))
		r.b.WriteString("\n")
	}
	r.b.WriteString("</code></pre>\n")
	return i
}

func (r *planRenderer) heading(line string) {
	m := mdHeadingRE.FindStringSubmatch(line)
	level := strconv.Itoa(len(m[1]))
	text := strings.TrimSpace(m[2])
	r.b.WriteString("<h" + level + ` id="` + r.headingID(text) + `">`)
	r.b.WriteString(inline(text))
	r.b.WriteString("</h" + level + ">\n")
}

// headingID gives a heading an id to link to. A task heading gets its task
// id (#T5.7), so a link to a task can be written by anyone who knows the id
// without knowing the title; every other heading gets a slug of its words. A
// repeat gets "-2", "-3" and so on. A slug holds no capital and no dot, so it
// can never be taken for a task's id.
func (r *planRenderer) headingID(text string) string {
	id := ""
	if f := strings.Fields(text); len(f) > 0 && masterplan.ValidID(f[0]) {
		id = f[0]
	} else {
		id = slug(text)
	}
	r.ids[id]++
	if n := r.ids[id]; n > 1 {
		// The numbered form is counted too, so "a", "a", "a-2" cannot give
		// the same id twice.
		for {
			next := id + "-" + strconv.Itoa(n)
			if r.ids[next] == 0 {
				r.ids[next] = 1
				return next
			}
			n++
		}
	}
	return id
}

// slug keeps a-z and 0-9 (capitals are lowered) and turns every run of
// anything else into one "-". The result needs no escaping.
func slug(text string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(text) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(c)
			continue
		}
		dash = true
	}
	if b.Len() == 0 {
		return "section"
	}
	return b.String()
}

// quote renders a run of ">" lines: the marker and one space are taken off
// each and what is left is rendered as blocks of its own, so a list or a
// second paragraph inside a quote works.
func (r *planRenderer) quote(lines []string, i, depth int) int {
	var inner []string
	for ; i < len(lines) && isQuote(lines[i]); i++ {
		l := strings.TrimPrefix(strings.TrimLeft(lines[i], " \t"), ">")
		inner = append(inner, strings.TrimPrefix(l, " "))
	}
	r.b.WriteString("<blockquote>\n")
	r.blocks(inner, depth+1)
	r.b.WriteString("</blockquote>\n")
	return i
}

// table renders a pipe table. A row shorter than the header is padded and a
// longer one keeps its extra cells, so no text from the plan is dropped. The
// wrapper lets a wide table scroll sideways on a phone instead of stretching
// the page.
func (r *planRenderer) table(lines []string, i int) int {
	head := splitCells(lines[i])
	i += 2 // the header and the separator
	r.b.WriteString(`<div class="scroll"><table>` + "\n<thead><tr>")
	for _, c := range head {
		r.b.WriteString("<th>" + inline(c) + "</th>")
	}
	r.b.WriteString("</tr></thead>\n<tbody>\n")
	for ; i < len(lines) && strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "|"); i++ {
		cells := splitCells(lines[i])
		for len(cells) < len(head) {
			cells = append(cells, "")
		}
		r.b.WriteString("<tr>")
		for _, c := range cells {
			r.b.WriteString("<td>" + inline(c) + "</td>")
		}
		r.b.WriteString("</tr>\n")
	}
	r.b.WriteString("</tbody>\n</table></div>\n")
	return i
}

// splitCells splits a table row into trimmed cells. A pipe written "\|" is a
// literal pipe, and a pipe inside a closed code span does not end a cell;
// masterplan reads the open-questions table the same way, so a row shows the
// cells the daemon sees.
func splitCells(line string) []string {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "|") {
		return nil
	}
	s = s[1:]
	var cells []string
	var cur strings.Builder
	closed := false // the last thing read was a cell-ending pipe
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '|':
			cur.WriteByte('|')
			closed = false
			i += 2
		case s[i] == '`':
			n := runLen(s, i)
			end := closingRun(s, i+n, n)
			if end < 0 {
				cur.WriteString(s[i : i+n])
				i += n
			} else {
				cur.WriteString(s[i : end+n])
				i = end + n
			}
			closed = false
		case s[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			closed = true
			i++
		default:
			cur.WriteByte(s[i])
			closed = false
			i++
		}
	}
	// Text after the last pipe is a cell of a row written without a closing
	// pipe. Nothing after it means the pipe closed the row.
	if rest := strings.TrimSpace(cur.String()); rest != "" || !closed {
		cells = append(cells, rest)
	}
	return cells
}

// mdItem is one list item: its text, and the items nested under it.
type mdItem struct {
	ordered  bool
	start    string
	text     []string
	children []mdItem
}

// indentWidth counts leading white space, a tab as four.
func indentWidth(s string) int {
	n := 0
	for _, c := range s {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// list renders a list that starts at lines[i]. An item indented two or more
// past the first item is nested under the item before it; anything deeper
// joins that same nested level, because a plan does not need more and one
// level keeps this simple. A blank line does not end the list when another
// item, or indented text, follows it.
func (r *planRenderer) list(lines []string, i int) int {
	first := mdItemRE.FindStringSubmatch(lines[i])
	base := indentWidth(first[1])
	ordered := isOrdered(first[2])
	var items []mdItem

	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j >= len(lines) {
				break
			}
			next := mdItemRE.FindStringSubmatch(lines[j])
			continues := false
			switch {
			case isFence(lines[j]) || isTableStart(lines, j) || isQuote(lines[j]):
			case next != nil:
				nested := indentWidth(next[1]) >= base+2
				continues = nested || (indentWidth(next[1]) >= base && isOrdered(next[2]) == ordered)
			default:
				continues = indentWidth(lines[j]) >= base+2
			}
			if !continues {
				break
			}
			i = j
			continue
		}
		if m := mdItemRE.FindStringSubmatch(line); m != nil {
			it := mdItem{ordered: isOrdered(m[2]), start: strings.TrimRight(m[2], ".)"), text: []string{m[3]}}
			ind := indentWidth(m[1])
			switch {
			case ind >= base+2 && len(items) > 0:
				last := &items[len(items)-1]
				last.children = append(last.children, it)
			case ind < base || it.ordered != ordered:
				// A less indented item, or a bullet after numbers, belongs
				// to another list.
				r.items(items, ordered)
				return i
			default:
				items = append(items, it)
			}
			i++
			continue
		}
		if startsBlock(lines, i) {
			break
		}
		// Any other line carries on the text of the item above it.
		last := &items[len(items)-1]
		if n := len(last.children); n > 0 {
			last.children[n-1].text = append(last.children[n-1].text, strings.TrimSpace(line))
		} else {
			last.text = append(last.text, strings.TrimSpace(line))
		}
		i++
	}
	r.items(items, ordered)
	return i
}

func isOrdered(marker string) bool {
	return len(marker) > 1
}

func (r *planRenderer) items(items []mdItem, ordered bool) {
	if len(items) == 0 {
		return
	}
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	r.b.WriteString("<" + tag)
	// A numbered list keeps the number it starts at, so a list a code block
	// cut in two does not start again from 1. Atoi is what makes the value
	// safe to write: only digits get through.
	if n, err := strconv.Atoi(items[0].start); ordered && err == nil && n != 1 {
		r.b.WriteString(` start="` + strconv.Itoa(n) + `"`)
	}
	r.b.WriteString(">\n")
	for _, it := range items {
		r.b.WriteString("<li>" + inline(strings.Join(it.text, "\n")))
		if len(it.children) > 0 {
			r.b.WriteString("\n")
			r.items(it.children, it.children[0].ordered)
		}
		r.b.WriteString("</li>\n")
	}
	r.b.WriteString("</" + tag + ">\n")
}

// paragraph joins lines up to a blank line or the start of another block.
// Past maxQuoteDepth a ">" line is no longer a block, so it lands here.
func (r *planRenderer) paragraph(lines []string, i, depth int) int {
	start := i
	for i++; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			break
		}
		if startsBlock(lines, i) && !(isQuote(lines[i]) && depth >= maxQuoteDepth) {
			break
		}
	}
	part := make([]string, 0, i-start)
	for _, l := range lines[start:i] {
		part = append(part, strings.TrimSpace(l))
	}
	r.b.WriteString("<p>" + inline(strings.Join(part, "\n")) + "</p>\n")
	return i
}

// runLen is how many backticks start at s[i].
func runLen(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// closingRun finds a run of exactly n backticks at or after from and returns
// where it starts, or -1. A code span opened with n backticks is closed only
// by a run of the same length, so a span can hold shorter runs.
func closingRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		m := runLen(s, i)
		if m == n {
			return i
		}
		i += m
	}
	return -1
}

// closingBold finds the "**" that closes a bold run opened just before from,
// skipping any code span on the way, or returns -1.
func closingBold(s string, from int) int {
	for i := from; i < len(s); {
		switch {
		case s[i] == '`':
			n := runLen(s, i)
			if end := closingRun(s, i+n, n); end >= 0 {
				i = end + n
			} else {
				i += n
			}
		case strings.HasPrefix(s[i:], "**"):
			return i
		default:
			i++
		}
	}
	return -1
}

// inline renders one piece of text: code spans, bold and bare links. Text
// between them, and inside them, is escaped; that is the only way text gets
// from the plan into the output.
func inline(s string) string {
	var b strings.Builder
	textStart := 0
	flush := func(end int) {
		b.WriteString(html.EscapeString(s[textStart:end]))
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			n := runLen(s, i)
			end := closingRun(s, i+n, n)
			if end < 0 {
				i += n // no closing run: the backticks are text
				continue
			}
			flush(i)
			code := s[i+n : end]
			// "`` `x` ``" is how markdown writes a span holding a backtick:
			// one space each side is padding, not content.
			if len(code) > 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
				code = code[1 : len(code)-1]
			}
			b.WriteString("<code>" + html.EscapeString(code) + "</code>")
			i = end + n
			textStart = i
		case strings.HasPrefix(s[i:], "**"):
			end := closingBold(s, i+2)
			// Bold must hug its words. "2 ** 3 ** 4" is arithmetic.
			if end <= i+2 || isSpaceByte(s[i+2]) || isSpaceByte(s[end-1]) {
				i += 2
				continue
			}
			flush(i)
			// The inner text holds no "**" outside a code span, so this
			// goes one level down and no further.
			b.WriteString("<strong>" + inline(s[i+2:end]) + "</strong>")
			i = end + 2
			textStart = i
		case s[i] == 'h' && (i == 0 || !isWordByte(s[i-1])):
			link := linkAt(s[i:])
			if link == "" {
				i++
				continue
			}
			flush(i)
			esc := html.EscapeString(link)
			b.WriteString(`<a href="` + esc + `" rel="noopener noreferrer">` + esc + `</a>`)
			i += len(link)
			textStart = i
		default:
			i++
		}
	}
	flush(len(s))
	return b.String()
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

func isWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// linkAt returns the web address s starts with, or "" when it does not start
// with one. An address runs to the first white space, control character or
// one of the characters that cannot be in one unescaped; punctuation at its
// end belongs to the sentence, except a ")" that closes a "(" inside the
// address.
//
// The scheme is checked twice on purpose: by the prefix, and again on what
// url.Parse read, with a host required. Only http and https ever become a
// link, so no text in a plan can make a link that runs code when it is
// tapped.
func linkAt(s string) string {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return ""
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if c <= ' ' || c == 0x7f || c == '<' || c == '>' || c == '"' || c == '`' || c == '|' || c == '\\' {
			break
		}
		end++
	}
	link := s[:end]
	for len(link) > 0 {
		last := link[len(link)-1]
		if strings.IndexByte(".,;:!?'*]}", last) >= 0 {
			link = link[:len(link)-1]
			continue
		}
		if last == ')' && strings.Count(link, "(") < strings.Count(link, ")") {
			link = link[:len(link)-1]
			continue
		}
		break
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return link
}
