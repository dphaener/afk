package main

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/dphaener/afk/internal/install"
	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/prompt"
)

// riskPhrase is the only answer the safety screen accepts on a computer used
// every day. It is typed, not a y/n, so it cannot be given by reflex or by
// --yes.
const riskPhrase = "I accept the risk"

// installKeepDone ends every failure's "what to do": a failed step undoes
// nothing, so the next run starts from where this one stopped.
const installKeepDone = "The steps already done stay done; run afk install again to go on."

type installPhase int

const (
	// installProbing and installWorking show a one-line "what is happening";
	// a command, a write or a probe is running in the background.
	installProbing installPhase = iota
	installWorking
	// installSafety is the safety screen. It comes before anything changes,
	// every run, --yes included.
	installSafety
	// installRefused is the safety screen when the machine cannot be set up
	// at all (root, unsupported system): the fix, and nothing else.
	installRefused
	installConfirm
	installForm
	installFailed
	installDone
)

// installModel is the screen of `afk install` and the runner behind it. The
// runner is a loop over install.Next: run the step's action, probe the
// machine again, and let install.Verify judge the step from that fresh
// probe. The re-probe is the only judge on purpose: a command that exits 0
// can still have changed nothing (a login the person abandoned, an apt
// mirror that skipped a package), and trusting the exit status would carry
// that silently into every later step.
//
// The model is a pointer so the continuations that the step actions hand to
// each asynchronous operation (see async and handover) change this model and
// not a copy.
type installModel struct {
	env   installEnv
	yes   bool
	width int

	phase   installPhase
	working string
	facts   install.Facts
	plan    install.Plan
	// accepted is held only in memory; see install.Facts.RiskAccepted.
	accepted bool

	phrase    string
	phraseErr string

	question   string
	detail     string
	defaultYes bool
	answer     func(bool) tea.Cmd

	form     *huh.Form
	formDone func() tea.Cmd

	// opSeq numbers each asynchronous operation; a result for any but the
	// latest one is dropped, so a late reply can never resume a step that has
	// moved on.
	opSeq   int
	pending func(installResult) tea.Cmd

	current install.Step
	ran     []install.StepID

	failTitle, failWhat, failTodo string

	report    string
	address   string
	checkNote string

	exitCode int
}

// installResult is what an asynchronous operation hands back.
type installResult struct {
	out   []byte
	err   error
	facts install.Facts
	value any
}

type installOpDone struct {
	seq int
	res installResult
}

func newInstallModel(env installEnv, yes bool) *installModel {
	return &installModel{env: env, yes: yes, phase: installProbing, working: "Looking at this machine…", exitCode: 1}
}

func (m *installModel) Init() tea.Cmd {
	return m.reprobe("Looking at this machine…", m.afterFirstProbe)
}

// async runs fn off the screen's goroutine and resumes with then. fn must
// only call env seams: it runs concurrently with Update, so it must not touch
// the model.
func (m *installModel) async(label string, fn func() installResult, then func(installResult) tea.Cmd) tea.Cmd {
	m.opSeq++
	seq := m.opSeq
	m.pending = then
	m.phase = installWorking
	m.working = label
	return func() tea.Msg { return installOpDone{seq: seq, res: fn()} }
}

// handover gives the terminal to argv and resumes with then. It is used for
// everything that may ask the person something itself: sudo's password,
// the agent CLI's and gh's sign-ins (which show a code or open a browser),
// and afk's own web password prompt. A command run with its output captured
// would hide those prompts and wait forever.
func (m *installModel) handover(argv []string, then func(error) tea.Cmd) tea.Cmd {
	m.opSeq++
	seq := m.opSeq
	m.pending = func(r installResult) tea.Cmd { return then(r.err) }
	m.phase = installWorking
	m.working = "Running " + strings.Join(argv, " ")
	return m.env.handover(argv, func(err error) tea.Msg { return installOpDone{seq: seq, res: installResult{err: err}} })
}

// reprobe reads the machine again and rebuilds the plan.
func (m *installModel) reprobe(label string, then func() tea.Cmd) tea.Cmd {
	probe := m.env.probe
	return m.async(label, func() installResult { return installResult{facts: probe()} }, func(r installResult) tea.Cmd {
		m.setFacts(r.facts)
		return then()
	})
}

func (m *installModel) setFacts(f install.Facts) {
	f.RiskAccepted = m.accepted
	m.facts = f
	m.plan = install.Build(f)
}

func (m *installModel) step(id install.StepID) install.Step {
	for _, s := range m.plan.Steps {
		if s.ID == id {
			return s
		}
	}
	return install.Step{ID: id}
}

func (m *installModel) afterFirstProbe() tea.Cmd {
	if m.step(install.StepSafety).State == install.Cannot {
		m.phase = installRefused
		return nil
	}
	m.phase = installSafety
	return nil
}

// ask shows a yes/no question. Under --yes the default is taken and the
// question is never shown, which is why a question whose safe answer is No
// (replacing a unit afk did not write) must default to No.
func (m *installModel) ask(question, detail string, defaultYes bool, then func(bool) tea.Cmd) tea.Cmd {
	if m.yes {
		return then(defaultYes)
	}
	m.phase = installConfirm
	m.question, m.detail, m.defaultYes, m.answer = question, detail, defaultYes, then
	return nil
}

// showForm runs a huh form inside this screen, themed like afk's other
// prompts. then runs once the form is submitted.
func (m *installModel) showForm(f *huh.Form, then func() tea.Cmd) tea.Cmd {
	f = f.WithTheme(huh.ThemeFunc(func(bool) *huh.Styles { return prompt.Theme() })).WithShowHelp(true)
	if m.width > 0 {
		f = f.WithWidth(min(m.width, 100))
	}
	m.phase = installForm
	m.form, m.formDone = f, then
	return f.Init()
}

// fail stops the run. Nothing is undone: every earlier step is a real change
// the person agreed to, and taking it back could fail half way too.
func (m *installModel) fail(title, what, todo string) tea.Cmd {
	m.phase = installFailed
	m.failTitle, m.failWhat, m.failTodo = title, what, todo
	m.exitCode = 1
	m.pending, m.answer, m.form = nil, nil, nil
	return tea.Quit
}

func (m *installModel) failStep(what, todo string) tea.Cmd {
	if todo == "" {
		todo = installKeepDone
	} else {
		todo += " " + installKeepDone
	}
	return m.fail(m.current.Title, what, todo)
}

func (m *installModel) declined() tea.Cmd {
	return m.failStep("You said no, so this step was not done.", "")
}

func (m *installModel) stopByKey() tea.Cmd {
	what := "You stopped afk install."
	if len(m.ran) == 0 {
		what += " Nothing was changed."
	}
	title := m.current.Title
	if title == "" {
		title = "afk install"
	}
	return m.fail(title, what, installKeepDone)
}

func (m *installModel) accept() tea.Cmd {
	m.accepted = true
	m.setFacts(m.facts)
	return m.next()
}

// next picks the next step from the current plan.
func (m *installModel) next() tea.Cmd {
	s, act := install.Next(m.plan)
	switch act.Kind {
	case install.ActionFinished:
		return m.finish()
	case install.ActionStop:
		m.current = s
		return m.fail(s.Title, s.Reason, s.Fix)
	}
	m.current = s
	// A step that was done earlier in this run and is to do again would be
	// run over and over; stop instead and say what changed.
	if slices.Contains(m.ran, s.ID) {
		return m.failStep("This step was done earlier in this run but is not done now. "+s.Reason, "")
	}
	return m.runStep(s)
}

// stepDone records the step, probes again and lets Verify decide.
func (m *installModel) stepDone() tea.Cmd {
	id := m.current.ID
	m.ran = append(m.ran, id)
	return m.reprobe("Checking that it worked…", func() tea.Cmd {
		if err := install.Verify(m.plan, id); err != nil {
			return m.failStep(err.Error(), "Fix that, then run afk install again.")
		}
		return m.next()
	})
}

func (m *installModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case installOpDone:
		if msg.seq != m.opSeq || m.pending == nil {
			return m, nil
		}
		then := m.pending
		m.pending = nil
		return m, then(msg.res)
	}
	if m.phase == installForm && m.form != nil {
		return m, m.updateForm(msg)
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if key.String() == "ctrl+c" {
		switch m.phase {
		case installFailed, installDone, installRefused:
			return m, tea.Quit
		}
		return m, m.stopByKey()
	}
	switch m.phase {
	case installSafety:
		return m, m.safetyKey(key)
	case installRefused:
		if key.String() == "enter" || key.String() == "esc" || key.String() == "q" {
			m.exitCode = 1
			return m, tea.Quit
		}
	case installConfirm:
		return m, m.confirmKey(key)
	}
	return m, nil
}

func (m *installModel) updateForm(msg tea.Msg) tea.Cmd {
	fm, cmd := m.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateAborted:
		m.form = nil
		return m.stopByKey()
	case huh.StateCompleted:
		then := m.formDone
		m.form, m.formDone = nil, nil
		return then()
	}
	return cmd
}

func (m *installModel) personalRisk() bool {
	return m.plan.Risk == install.RiskPersonal || m.plan.Risk == install.RiskUnknown
}

func (m *installModel) safetyKey(key tea.KeyPressMsg) tea.Cmd {
	if key.String() == "esc" {
		return m.stopByKey()
	}
	if !m.personalRisk() {
		if key.String() == "enter" {
			return m.accept()
		}
		return nil
	}
	switch key.String() {
	case "enter":
		if m.phrase == riskPhrase {
			return m.accept()
		}
		m.phrase = ""
		m.phraseErr = "That is not the phrase. Type " + riskPhrase + " exactly, or press ctrl+c to stop."
	case "backspace":
		if r := []rune(m.phrase); len(r) > 0 {
			m.phrase = string(r[:len(r)-1])
		}
	default:
		if key.Text != "" {
			m.phrase += key.Text
			m.phraseErr = ""
		}
	}
	return nil
}

func (m *installModel) confirmKey(key tea.KeyPressMsg) tea.Cmd {
	var yes bool
	switch strings.ToLower(key.String()) {
	case "y":
		yes = true
	case "n":
		yes = false
	case "enter":
		yes = m.defaultYes
	case "esc":
		return m.stopByKey()
	default:
		return nil
	}
	then := m.answer
	m.answer = nil
	if then == nil {
		return nil
	}
	return then(yes)
}

// finish is the last step of every run, a run where everything was already
// done included: it reads the daemon's status, the stored usage reading and
// the page's address, and quits with them on screen.
func (m *installModel) finish() tea.Cmd {
	m.current = m.step(install.StepCheck)
	env := m.env
	return m.async("Checking everything…", func() installResult {
		report := ""
		if env.checkReport != nil {
			report = env.checkReport()
		}
		addr, note := "", ""
		cfg, err := env.loadConfig()
		if err != nil {
			note = fmt.Sprintf("afk could not read its config file, so it cannot say where the web page is: %v", err)
		} else {
			ip := ""
			if env.hostIP != nil {
				ip = env.hostIP()
			}
			addr = install.WebAddress(cfg.Web.Bind, cfg.Web.PublicURL, ip)
		}
		return installResult{value: [3]string{report, addr, note}}
	}, func(r installResult) tea.Cmd {
		v, _ := r.value.([3]string)
		m.report, m.address, m.checkNote = v[0], v[1], v[2]
		m.phase = installDone
		m.exitCode = 0
		return tea.Quit
	})
}

// View styles. Colour carries no meaning on its own: every mark is also a
// distinct character, so the screen reads the same without colour.
var (
	installTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(palette.Emphasis))
	installDoneStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Success))
	installErrStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Error))
	installMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Muted))
	installBoldStyle  = lipgloss.NewStyle().Bold(true)
	installBoxStyle   = lipgloss.NewStyle().Bold(true).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(palette.Success)).
				Padding(1, 4)
)

func (m *installModel) viewWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	return min(w, 120)
}

// wrap breaks text to fit width, indenting every line by indent spaces.
func installWrap(text string, width, indent int) string {
	w := max(width-indent, 20)
	out := lipgloss.NewStyle().Width(w).Render(text)
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(pad+l, " ")
	}
	return strings.Join(lines, "\n")
}

// installStyled styles each line on its own. lipgloss pads a multi-line
// block to its widest line, which would leave trailing spaces on every
// shorter line of a wrapped paragraph.
func installStyled(st lipgloss.Style, text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = st.Render(l)
	}
	return strings.Join(lines, "\n")
}

// renderInstallChecklist is the list of steps at the top of every screen:
// done steps ticked and the step at work marked. Only the step at work and a
// step that cannot be done carry their reason: with all twelve reasons the
// list alone is taller than a 24-row terminal, and the question below it
// would scroll out of sight. --dry-run prints every reason.
func renderInstallChecklist(p install.Plan, current install.StepID, width int) string {
	var b strings.Builder
	for _, s := range p.Steps {
		mark := installMutedStyle.Render("○")
		switch s.State {
		case install.Done:
			mark = installDoneStyle.Render("✓")
		case install.Cannot:
			mark = installErrStyle.Render("✗")
		}
		lead := "  "
		if s.ID == current && s.State != install.Done {
			lead = "▸ "
		}
		b.WriteString(lead + mark + " " + s.Title + "\n")
		if s.State == install.Cannot || (s.ID == current && s.State != install.Done) {
			b.WriteString(installStyled(installMutedStyle, installWrap(s.Reason, width, 6)) + "\n")
		}
		if s.State == install.Cannot && s.Fix != "" {
			b.WriteString(installWrap("To fix: "+s.Fix, width, 6) + "\n")
		}
	}
	return b.String()
}

func (m *installModel) View() tea.View {
	return tea.NewView(m.render())
}

func (m *installModel) render() string {
	w := m.viewWidth()
	var b strings.Builder
	b.WriteString(installTitleStyle.Render("afk install") + "\n\n")
	// The refusal shows the fix and nothing else: twelve "cannot" lines
	// would bury the one thing that has to change.
	if len(m.plan.Steps) > 0 && m.phase != installRefused {
		b.WriteString(renderInstallChecklist(m.plan, m.current.ID, w))
		b.WriteString("\n")
	}
	switch m.phase {
	case installProbing, installWorking:
		b.WriteString("… " + m.working + "\n")
	case installSafety:
		b.WriteString(m.renderSafety(w))
	case installRefused:
		b.WriteString(installBoldStyle.Render("afk install cannot set up this machine.") + "\n\n")
		b.WriteString(installWrap(m.step(install.StepSafety).Fix, w, 0) + "\n\n")
		b.WriteString(installMutedStyle.Render("Nothing was changed. Press enter to leave.") + "\n")
	case installConfirm:
		b.WriteString(installStyled(installBoldStyle, installWrap(m.question, w, 0)) + "\n")
		if m.detail != "" {
			b.WriteString("\n" + installWrap(m.detail, w, 2) + "\n")
		}
		choice := "[Y/n]"
		if !m.defaultYes {
			choice = "[y/N]"
		}
		b.WriteString("\n" + installMutedStyle.Render("Press y or n, or enter for the default "+choice+".") + "\n")
	case installForm:
		if m.form != nil {
			b.WriteString(m.form.View() + "\n")
		}
	case installFailed:
		b.WriteString(installErrStyle.Render("✗ Stopped at: "+m.failTitle) + "\n\n")
		b.WriteString(installBoldStyle.Render("What happened") + "\n" + installWrap(m.failWhat, w, 2) + "\n\n")
		if m.failTodo != "" {
			b.WriteString(installBoldStyle.Render("What to do") + "\n" + installWrap(m.failTodo, w, 2) + "\n")
		}
	case installDone:
		b.WriteString(m.renderDone(w))
	}
	return b.String()
}

func (m *installModel) renderSafety(w int) string {
	var b strings.Builder
	b.WriteString(installBoldStyle.Render("Before anything changes") + "\n\n")
	b.WriteString(installWrap("afk runs AI agents with their permission prompts turned off. They can run any command, "+
		"change any file and use every login on this machine without asking first. That is why afk belongs on a machine "+
		"set aside for it: a virtual machine, or a computer you use for nothing else.", w, 0) + "\n\n")
	b.WriteString(installWrap(installRiskText(m.plan.Risk), w, 0) + "\n\n")
	if !m.personalRisk() {
		b.WriteString(installMutedStyle.Render("Press enter to go on, or ctrl+c to stop.") + "\n")
		return b.String()
	}
	b.WriteString(installWrap("The safer choice is a Multipass virtual machine: install Multipass, run multipass launch "+
		"--name afk, then multipass shell afk, and run afk install in there.", w, 0) + "\n\n")
	b.WriteString(installWrap("To go on here anyway, type "+riskPhrase+" and press enter. "+
		"--yes does not answer this; only you can.", w, 0) + "\n\n")
	b.WriteString("> " + m.phrase + "█\n")
	if m.phraseErr != "" {
		b.WriteString(installStyled(installErrStyle, installWrap(m.phraseErr, w, 0)) + "\n")
	}
	return b.String()
}

func (m *installModel) renderDone(w int) string {
	var b strings.Builder
	switch {
	case len(m.ran) == 0 && m.step(install.StepCheck).State == install.Done:
		b.WriteString(installDoneStyle.Render("Everything is in place. Nothing was changed.") + "\n\n")
	case len(m.ran) == 0:
		b.WriteString("Nothing was changed.\n\n")
	default:
		b.WriteString(installDoneStyle.Render("afk is set up.") + "\n\n")
	}
	if s := m.step(install.StepCheck); s.State != install.Done && s.Reason != "" {
		b.WriteString(installWrap(s.Reason, w, 0) + "\n\n")
	}
	if r := strings.TrimRight(m.report, "\n"); r != "" {
		b.WriteString(r + "\n\n")
	}
	if m.checkNote != "" {
		b.WriteString(installStyled(installErrStyle, installWrap(m.checkNote, w, 0)) + "\n")
		return b.String()
	}
	b.WriteString("The web page is at:\n")
	b.WriteString(installBoxStyle.Render(m.address) + "\n\n")
	b.WriteString(installWrap("Open it in a browser, sign in with the web page password, and press Plan a new project.", w, 0) + "\n")
	return b.String()
}
