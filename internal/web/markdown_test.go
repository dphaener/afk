package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// allowedTagRE matches every tag renderPlan may write, and nothing else. The
// attribute values are spelled out as tightly as the renderer makes them, so
// an attribute a plan managed to add would leave a "<" behind for the test
// to find.
var allowedTagRE = regexp.MustCompile(`</?(?:p|ul|ol|li|pre|code|strong|blockquote|table|thead|tbody|tr|th|td|div|a|h[1-6])>` +
	`|<hr>` +
	`|<h[1-6] id="[A-Za-z0-9.-]+">` +
	`|<ol start="[0-9]+">` +
	`|<div class="scroll">` +
	`|<a href="https?://[^"<>]*" rel="noopener noreferrer">`)

// onlyAllowedMarkup fails the test unless out is made of the renderer's own
// tags around text with no "<" or ">" left in it. That is the whole safety
// claim of renderPlan, checked from the outside: whatever the plan said, the
// only tags in the output are the ones the renderer wrote.
func onlyAllowedMarkup(t *testing.T, out string) {
	t.Helper()
	rest := allowedTagRE.ReplaceAllString(out, "")
	if i := strings.IndexAny(rest, "<>"); i >= 0 {
		from, to := max(i-60, 0), min(i+60, len(rest))
		t.Errorf("the output holds markup the renderer does not write, near: %q", rest[from:to])
	}
}

// TestRenderPlan has one row for each construct the plan view draws.
//
// Load-bearing rows: "task heading" (the id is the task's id, which is what
// a link to a task uses), "repeated heading" (two headings must not share an
// id), "fenced code is verbatim" (markdown inside a fence is not rendered,
// and a "#" line in it is not a heading, matching what the checker reads),
// "pipe in a code span" and "escaped pipe" (a table cell must split where
// the daemon's reader splits it), and "lone pipe line" (not a table, so no
// text is lost to a table that never comes).
func TestRenderPlan(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{name: "empty", in: "", want: nil, notWant: []string{"<p>"}},
		{name: "heading levels", in: "# One\n## Two\n### Three\n#### Four\n",
			want: []string{`<h1 id="one">One</h1>`, `<h2 id="two">Two</h2>`, `<h3 id="three">Three</h3>`, `<h4 id="four">Four</h4>`}},
		{name: "task heading", in: "#### T5.7 — Approve and go\n",
			want: []string{`<h4 id="T5.7">T5.7 — Approve and go</h4>`}},
		{name: "heading slug", in: "## 10. Open questions (to resolve)\n",
			want: []string{`<h2 id="10-open-questions-to-resolve">`}},
		{name: "repeated heading", in: "## Notes\n## Notes\n## Notes\n",
			want: []string{`id="notes"`, `id="notes-2"`, `id="notes-3"`}},
		{name: "repeated task heading", in: "#### T1.1 — A\n#### T1.1 — B\n",
			want: []string{`id="T1.1"`, `id="T1.1-2"`}},
		{name: "heading with no letters", in: "## ???\n", want: []string{`<h2 id="section">???</h2>`}},
		{name: "heading needs a space", in: "#hashtag\n", want: []string{"<p>#hashtag</p>"}, notWant: []string{"<h1"}},
		{name: "paragraphs", in: "one\ntwo\n\nthree\n", want: []string{"<p>one\ntwo</p>", "<p>three</p>"}},
		{name: "bullet list", in: "- a\n- b\n", want: []string{"<ul>\n<li>a</li>\n<li>b</li>\n</ul>"}},
		{name: "nested list", in: "- a\n  - a1\n  - a2\n- b\n",
			want: []string{"<ul>\n<li>a\n<ul>\n<li>a1</li>\n<li>a2</li>\n</ul>\n</li>\n<li>b</li>\n</ul>"}},
		{name: "list item carried onto a second line", in: "- first line\n  second line\n- b\n",
			want: []string{"<li>first line\nsecond line</li>"}},
		{name: "deeper nesting joins the one nested level", in: "- a\n  - a1\n    - deep\n",
			want: []string{"<li>a1</li>\n<li>deep</li>"}},
		{name: "list after a paragraph", in: "Intro:\n- a\n", want: []string{"<p>Intro:</p>", "<li>a</li>"}},
		{name: "list with a blank line between items", in: "- a\n\n- b\n", want: []string{"<ul>\n<li>a</li>\n<li>b</li>\n</ul>"}},
		{name: "numbered list", in: "1. a\n2. b\n", want: []string{"<ol>\n<li>a</li>\n<li>b</li>\n</ol>"}},
		{name: "numbered list keeps its start", in: "3. c\n4. d\n", want: []string{`<ol start="3">`}},
		{name: "bullets after numbers are a second list", in: "1. a\n- b\n", want: []string{"<ol>\n<li>a</li>\n</ol>", "<ul>\n<li>b</li>\n</ul>"}},
		{name: "fenced code is verbatim", in: "```go\n# not a heading\n**not bold** <b>\n- not a list\n```\nafter\n",
			want:    []string{"<pre><code># not a heading\n**not bold** &lt;b&gt;\n- not a list\n</code></pre>", "<p>after</p>"},
			notWant: []string{"<h1", "<strong>", "<li>", "go\n#"}},
		{name: "tilde fence", in: "~~~\ncode\n~~~\n", want: []string{"<pre><code>code\n</code></pre>"}},
		{name: "unclosed fence runs to the end", in: "```\na\n\nb", want: []string{"<pre><code>a\n\nb\n</code></pre>"}},
		{name: "fence keeps indentation", in: "```\n  two\n\tindented\n```\n", want: []string{"  two\n\tindented\n"}},
		{name: "table", in: "| Topic | Decision |\n|---|---|\n| Language | Go |\n",
			want: []string{`<div class="scroll"><table>`, "<thead><tr><th>Topic</th><th>Decision</th></tr></thead>", "<tr><td>Language</td><td>Go</td></tr>", "</table></div>"}},
		{name: "table with alignment colons", in: "| a | b |\n|:--|--:|\n| 1 | 2 |\n", want: []string{"<td>1</td><td>2</td>"}},
		{name: "short row is padded", in: "| a | b |\n|---|---|\n| 1 |\n", want: []string{"<tr><td>1</td><td></td></tr>"}},
		{name: "long row keeps its cells", in: "| a |\n|---|\n| 1 | 2 |\n", want: []string{"<tr><td>1</td><td>2</td></tr>"}},
		{name: "pipe in a code span", in: "| a | b |\n|---|---|\n| `x | y` | z |\n", want: []string{"<tr><td><code>x | y</code></td><td>z</td></tr>"}},
		{name: "escaped pipe", in: "| a | b |\n|---|---|\n| x \\| y | z |\n", want: []string{"<tr><td>x | y</td><td>z</td></tr>"}},
		{name: "lone pipe line", in: "| just a line |\n", want: []string{"<p>| just a line |</p>"}, notWant: []string{"<table"}},
		{name: "table ends at a blank line", in: "| a |\n|---|\n| 1 |\n\nafter\n", want: []string{"</table></div>\n<p>after</p>"}},
		{name: "block quote", in: "> one\n> two\n>\n> three\n", want: []string{"<blockquote>\n<p>one\ntwo</p>\n<p>three</p>\n</blockquote>"}},
		{name: "list in a block quote", in: "> - a\n> - b\n", want: []string{"<blockquote>\n<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n</blockquote>"}},
		{name: "rule", in: "a\n\n---\n\nb\n", want: []string{"<p>a</p>\n<hr>\n<p>b</p>"}},
		{name: "rule right under a paragraph", in: "a\n---\nb\n", want: []string{"<p>a</p>\n<hr>\n<p>b</p>"}},
		{name: "bold", in: "a **b c** d\n", want: []string{"<p>a <strong>b c</strong> d</p>"}},
		{name: "bold label", in: "- **Goal:** g\n", want: []string{"<li><strong>Goal:</strong> g</li>"}},
		{name: "unclosed bold is text", in: "a **b\n", want: []string{"<p>a **b</p>"}, notWant: []string{"<strong>"}},
		{name: "stars with spaces are not bold", in: "2 ** 3 ** 4\n", want: []string{"<p>2 ** 3 ** 4</p>"}},
		{name: "code span", in: "run `afk plan lint` now\n", want: []string{"run <code>afk plan lint</code> now"}},
		{name: "code span keeps markup characters as text", in: "`**x** <i>`\n", want: []string{"<code>**x** &lt;i&gt;</code>"}, notWant: []string{"<strong>"}},
		{name: "code span with a backtick inside", in: "`` a`b ``\n", want: []string{"<code>a`b</code>"}},
		{name: "unclosed backtick is text", in: "a ` b\n", want: []string{"<p>a ` b</p>"}},
		{name: "code inside bold", in: "**see `x`**\n", want: []string{"<strong>see <code>x</code></strong>"}},
		{name: "bold does not close inside a code span", in: "**a `**` b**\n", want: []string{"<strong>a <code>**</code> b</strong>"}},
		{name: "bare link", in: "see https://example.com/a?b=1&c=2 now\n",
			want: []string{`see <a href="https://example.com/a?b=1&amp;c=2" rel="noopener noreferrer">https://example.com/a?b=1&amp;c=2</a> now`}},
		{name: "link leaves the sentence's full stop out", in: "See http://example.com/x.\n", want: []string{`<a href="http://example.com/x" rel="noopener noreferrer">http://example.com/x</a>.`}},
		{name: "link in brackets", in: "(see https://example.com/x)\n", want: []string{`href="https://example.com/x"`, "</a>)"}},
		{name: "link keeps its own brackets", in: "https://example.com/a_(b)\n", want: []string{`href="https://example.com/a_(b)"`}},
		{name: "link in a code span is not a link", in: "`https://example.com`\n", want: []string{"<code>https://example.com</code>"}, notWant: []string{"<a "}},
		{name: "link with no host is text", in: "https:// is a scheme\n", want: []string{"<p>https:// is a scheme</p>"}, notWant: []string{"<a "}},
		{name: "a word ending in http is not a link", in: "xhttp://example.com\n", want: []string{"<p>xhttp://example.com</p>"}, notWant: []string{"<a "}},
		{name: "windows line endings", in: "# A\r\n\r\ntext\r\n", want: []string{`<h1 id="a">A</h1>`, "<p>text</p>"}, notWant: []string{"\r"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(renderPlan(tc.in))
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("output does not contain %q\ngot:\n%s", w, got)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("output contains %q, which it should not\ngot:\n%s", w, got)
				}
			}
			onlyAllowedMarkup(t, got)
		})
	}
}

// TestRenderPlanHostileInput is the reason renderPlan may mark its result
// safe: a plan is written by an agent and may quote anything, and none of it
// may reach the page as markup.
//
// Every row is load-bearing. Each puts markup somewhere a different part of
// the renderer handles (a paragraph, a heading and its id, a table cell, a
// list item, a block quote, a code span, a fence, bold, a link's address),
// because the escaping is done in each of those places and a row proves only
// the place it goes through. For each, the output must hold no tag but the
// renderer's own (onlyAllowedMarkup), and the hostile text must still be
// there as text: hiding it would be a second way to mislead the owner.
func TestRenderPlanHostileInput(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{name: "script tag in a paragraph", in: "<script>alert(1)</script>\n",
			want: []string{"&lt;script&gt;alert(1)&lt;/script&gt;"}, notWant: []string{"<script"}},
		{name: "script tag in a heading", in: "## <script>alert(1)</script>\n",
			want: []string{`<h2 id="script-alert-1-script">&lt;script&gt;alert(1)&lt;/script&gt;</h2>`}, notWant: []string{"<script"}},
		{name: "onclick attribute", in: `<a href="#" onclick="alert(1)">tap</a>` + "\n",
			want: []string{"&lt;a href=&#34;#&#34; onclick=&#34;alert(1)&#34;&gt;tap&lt;/a&gt;"}, notWant: []string{"<a ", `onclick="`}},
		{name: "quote in a heading cannot leave the id", in: "## x\" onclick=\"alert(1)\n",
			want: []string{`<h2 id="x-onclick-alert-1">x&#34; onclick=&#34;alert(1)</h2>`}, notWant: []string{`onclick="`}},
		{name: "javascript link written bare", in: "javascript:alert(1)\n",
			want: []string{"<p>javascript:alert(1)</p>"}, notWant: []string{"<a "}},
		{name: "javascript link in markdown link form", in: "[tap](javascript:alert(1))\n",
			want: []string{"[tap](javascript:alert(1))"}, notWant: []string{"<a ", "href"}},
		{name: "javascript link in an html anchor", in: `<a href="javascript:alert(1)">tap</a>` + "\n",
			want: []string{"&lt;a href=&#34;javascript:alert(1)&#34;&gt;"}, notWant: []string{"<a "}},
		{name: "data link", in: "data:text/html,<script>alert(1)</script>\n", notWant: []string{"<a ", "<script"}},
		{name: "quote in a link cannot leave the href", in: `https://example.com/"onmouseover="alert(1)` + "\n",
			want: []string{`<a href="https://example.com/" rel="noopener noreferrer">https://example.com/</a>&#34;onmouseover=&#34;alert(1)`}, notWant: []string{`onmouseover="`}},
		{name: "angle bracket ends a link", in: "https://example.com/<script>alert(1)</script>\n",
			want: []string{`href="https://example.com/"`, "&lt;script&gt;"}, notWant: []string{"<script"}},
		{name: "single quote in a link", in: "https://example.com/a'b=c\n", want: []string{`href="https://example.com/a&#39;b=c"`}},
		{name: "table cell with b", in: "| a | b |\n|---|---|\n| <b>bold</b> | <img src=x onerror=alert(1)> |\n",
			want: []string{"<td>&lt;b&gt;bold&lt;/b&gt;</td>", "<td>&lt;img src=x onerror=alert(1)&gt;</td>"}, notWant: []string{"<b>", "<img"}},
		{name: "table header with markup", in: "| <i>a</i> |\n|---|\n| 1 |\n", want: []string{"<th>&lt;i&gt;a&lt;/i&gt;</th>"}, notWant: []string{"<i>"}},
		{name: "list item", in: "- <script>alert(1)</script>\n  - <iframe src=x>\n",
			want: []string{"<li>&lt;script&gt;", "<li>&lt;iframe src=x&gt;"}, notWant: []string{"<script", "<iframe"}},
		{name: "block quote", in: "> <script>alert(1)</script>\n", want: []string{"<blockquote>\n<p>&lt;script&gt;"}, notWant: []string{"<script"}},
		{name: "code span", in: "`</code><script>alert(1)</script>`\n",
			want: []string{"<code>&lt;/code&gt;&lt;script&gt;alert(1)&lt;/script&gt;</code>"}, notWant: []string{"<script"}},
		{name: "fence cannot be closed by a tag", in: "```\n</code></pre><script>alert(1)</script>\n```\n",
			want: []string{"<pre><code>&lt;/code&gt;&lt;/pre&gt;&lt;script&gt;alert(1)&lt;/script&gt;\n</code></pre>"}, notWant: []string{"<script"}},
		{name: "fence language word is not written", in: "```\"><script>alert(1)</script>\nx\n```\n",
			want: []string{"<pre><code>x\n</code></pre>"}, notWant: []string{"script"}},
		{name: "bold", in: "**<script>alert(1)</script>**\n", want: []string{"<strong>&lt;script&gt;alert(1)&lt;/script&gt;</strong>"}, notWant: []string{"<script"}},
		{name: "ampersand entity is not decoded", in: "&lt;script&gt; &amp; &#60;\n", want: []string{"&amp;lt;script&amp;gt; &amp;amp; &amp;#60;"}},
		{name: "numbered list start is only digits", in: "7\" onclick=\"x. a\n", notWant: []string{"<ol", `onclick="`}},
		{name: "style tag", in: "<style>body{display:none}</style>\n", want: []string{"&lt;style&gt;"}, notWant: []string{"<style"}},
		{name: "very deep block quote", in: strings.Repeat(">", 5000) + " <script>x</script>\n", notWant: []string{"<script"}},
		{name: "null byte and control characters", in: "a\x00<script>\x1b[0m\n", notWant: []string{"<script"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(renderPlan(tc.in))
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("output does not contain %q\ngot:\n%s", w, got)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(strings.ToLower(got), strings.ToLower(w)) {
					t.Errorf("output contains %q, which it should not\ngot:\n%s", w, got)
				}
			}
			onlyAllowedMarkup(t, got)
		})
	}
}

// TestRenderPlanThisRepositorysPlan renders the real master plan of this
// repository, the largest and oddest plan there is to try: 1,800 lines with
// tables, fences, nested lists, block quotes and inline HTML-looking text.
// It reads the file by a path relative to the package folder, so it is the
// plan in this checkout and no other.
//
// It is the plan view's acceptance in small: the task's own heading is there
// with its id, the tables are tables, and nothing in a file this varied gets
// markup past the renderer.
func TestRenderPlanThisRepositorysPlan(t *testing.T) {
	data, err := os.ReadFile("../../docs/PLAN.md")
	if err != nil {
		t.Fatalf("reading this repository's plan: %v", err)
	}
	got := string(renderPlan(string(data)))

	for _, w := range []string{
		`<h4 id="T5.7">T5.7 — Approve and go</h4>`,
		"<table",
		"<thead><tr><th>Question</th><th>Needed by</th></tr></thead>",
		"<pre><code>",
		"<blockquote>",
		"<strong>Depends on:</strong>",
		"<ol>",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("the rendered plan does not contain %q", w)
		}
	}
	if strings.Contains(strings.ToLower(got), "<script") {
		t.Errorf("the rendered plan holds a script tag")
	}
	onlyAllowedMarkup(t, got)

	// Every task heading the daemon reads has its own id on the page.
	ids := regexp.MustCompile(` id="([^"]+)"`).FindAllStringSubmatch(got, -1)
	seen := map[string]bool{}
	for _, m := range ids {
		if seen[m[1]] {
			t.Errorf("the id %q is given to two headings", m[1])
		}
		seen[m[1]] = true
	}
	for _, id := range []string{"T0.1", "T2.4", "T5.7"} {
		if !seen[id] {
			t.Errorf("no heading has the id %q", id)
		}
	}
}
