package web

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/provider"
)

// ChatView is the planning chat as GET /api/p/{project}/plan/chat and its
// page twin answer it. The chat is embedded, so its fields are encoded at the
// top level exactly as planchat.Chat encodes them on its own: the API's JSON
// is the same bytes it was before the page existed. Page is only for the
// page, and only filled for it.
type ChatView struct {
	planchat.Chat
	// Page is the chat screen laid out (see chatPageFor). The JSON leaves
	// it out, so an API reader never pays for the reads it needs.
	Page chatPage `json:"-"`
}

// chatPage is what plan_chat.html draws besides the chat itself: every
// decision the screen makes, so the template only chooses between them.
type chatPage struct {
	// State is which of the five screens to draw: one of the planchat
	// states as a string. A state this page does not know is drawn as
	// unknown, never as one that offers a reply.
	State string
	// Sub is the line under the title, the planning session's mode and phase
	// as its state file records them ("New plan · Rounds"), or "".
	Sub string
	// Steps are the phases of the session's mode, for the desktop's left
	// column; nil when there is no session state to place them by.
	Steps []chatStep
	// Folder is the project's main checkout and Repository where it is
	// pushed ("None set" when nowhere).
	Folder, Repository string
	// Overview is the planning session's overview screen, or "" when there
	// is no session to show (its screen would only say it is not found).
	Overview string
	// Action is the address every form on the screen posts to.
	Action string
	// Thread is the messages to draw, in order (see chatPageFor for the one
	// that is left out).
	Thread []chatMessage
	// Working is true while the agent works: the thread ends with the
	// working line and the live bar reads Working.
	Working bool
	// Problem is the unknown state's sentence, shown in a notice.
	Problem string
	// Questions are the pending questions, options in drawing order.
	Questions []chatQuestion
	// OneTap is true for a single single-select question: each option is a
	// submit button, so one press sends it with or without the script.
	OneTap bool
	// OwnAnswer is whether the "Or answer in your own words" field is
	// drawn. It is offered exactly where planchat accepts an own answer.
	OwnAnswer bool
	// Composer is whether the reply box is drawn, ComposerOn whether it
	// takes a reply, and ComposerHint the sentence that says why not.
	Composer     bool
	ComposerOn   bool
	ComposerHint string
	// Error is a refused reply's own sentence, shown after a form post that
	// was refused; Text and Other are what the owner had typed, put back in
	// their boxes so a refusal never costs them their words.
	Error       string
	Text, Other string
	// HasPlan is whether the empty state offers the three buttons; Start
	// holds them, the same rows the project page's Change the plan section
	// draws.
	HasPlan bool
	Start   []changeRow
}

// chatStep is one phase in the steps column.
type chatStep struct {
	Name          string
	Done, Current bool
}

// chatMessage is one message of the thread. Agent says which side it is on;
// Questions are an earlier question's options, shown read-only.
type chatMessage struct {
	Agent     bool
	Text      string
	Questions []chatQuestion
}

// chatQuestion is one question with its options in the order they are drawn.
// Index is its place among the pending questions, the i of the form's
// choice-<i> field.
type chatQuestion struct {
	Index       int
	Question    string
	Header      string
	MultiSelect bool
	Options     []provider.Option
}

// chatPageInput is everything chatPageFor reads. The handler does the reading
// and passes the results in, so every branch is a table row.
type chatPageInput struct {
	Project string
	Chat    planchat.Chat
	// Status is the planning session's state file as Deps.PlanStatus or
	// plansession.ReadState gave it.
	Status             PlanStatus
	Folder, Repository string
	// Error, Text and Other come from a refused form post (see chatPage).
	Error, Text, Other string
	// HasPlan is whether the project has a plan to change; the empty state
	// offers the three ways to start a session only then.
	HasPlan bool
}

// planningSessionName is the planning session's name, whose overview the
// screen links to. It is written out here, as live.go writes its folder,
// because this package does not import the session package.
const planningSessionName = "master-plan"

// The composer's sentences when it does not take a reply. They say why and
// what to do, and never what afk does inside.
const (
	composerWorking = "The agent is working. You can reply when it asks."
	composerChoice  = "Answer the question above."
	composerUnknown = "afk cannot tell what the planning session is waiting for, so you cannot reply here. Open it in afk dash to answer it there."
)

// chatPageFor lays out the chat screen. Pure.
//
// The thread leaves out one message: while a choice is pending, the agent's
// last question is drawn as the question card in the reply area, and drawing
// it in the thread as well would show the same question twice, once with
// buttons and once without. Every earlier question stays in the thread,
// read-only, so the owner can see what they were asked and what they picked.
//
// The composer is drawn in every state with a session, and only takes a reply
// while the agent waits for typed text: that is the one state planchat.Check
// accepts text in, and a box that looks ready but is always refused would
// teach the owner the page does not work. With no session the reply area
// holds the "No planning session" block instead, since there is nobody to
// write to.
func chatPageFor(in chatPageInput) chatPage {
	chat := in.Chat
	pg := chatPage{
		State:      string(chat.State),
		Folder:     in.Folder,
		Repository: in.Repository,
		Overview:   sessionBase(in.Project, planningSessionName),
		Action:     chatPath(in.Project),
		Error:      in.Error,
		Text:       in.Text,
		Other:      in.Other,
		HasPlan:    in.HasPlan,
		Start:      changeSectionFor(in.Project, in.HasPlan, false, plansession.State{}).Rows,
	}
	if pg.Repository == "" {
		pg.Repository = noRepository
	}
	switch chat.State {
	case planchat.StateNone, planchat.StateWorking, planchat.StateWaitingForText, planchat.StateWaitingForChoice:
	default:
		pg.State = string(planchat.StateUnknown)
	}

	st := in.Status
	if st.Known && st.Session && !st.Damaged {
		pg.Sub = chatSub(st.Mode, st.Phase)
		pg.Steps = chatSteps(st.Mode, st.Phase)
	}

	skip := -1
	if pg.State == string(planchat.StateWaitingForChoice) {
		for i := len(chat.Messages) - 1; i >= 0; i-- {
			if len(chat.Messages[i].Questions) > 0 {
				skip = i
				break
			}
		}
	}
	for i, m := range chat.Messages {
		if i == skip {
			continue
		}
		cm := chatMessage{Agent: m.Who != provider.WhoOwner, Text: m.Text}
		for j, q := range m.Questions {
			cm.Questions = append(cm.Questions, chatQuestionFor(j, q))
		}
		pg.Thread = append(pg.Thread, cm)
	}

	switch planchat.State(pg.State) {
	case planchat.StateNone:
		pg.Overview = ""
	case planchat.StateWorking:
		pg.Working = true
		pg.Composer, pg.ComposerHint = true, composerWorking
	case planchat.StateWaitingForText:
		pg.Composer, pg.ComposerOn = true, true
	case planchat.StateWaitingForChoice:
		for i, q := range chat.Questions {
			pg.Questions = append(pg.Questions, chatQuestionFor(i, q))
		}
		single := len(chat.Questions) == 1 && !chat.Questions[0].MultiSelect
		pg.OneTap, pg.OwnAnswer = single, single
		pg.Composer, pg.ComposerHint = true, composerChoice
	default:
		pg.Problem = chat.Problem
		if pg.Problem == "" {
			pg.Problem = "afk cannot tell what the planning session is doing."
		}
		pg.Composer, pg.ComposerHint = true, composerUnknown
	}
	return pg
}

// chatQuestionFor puts the recommended options first, keeping the agent's
// order otherwise, as the inbox does with its recommended answer: the owner
// usually takes it, and it should be the first thing their thumb reaches.
func chatQuestionFor(i int, q provider.Question) chatQuestion {
	cq := chatQuestion{Index: i, Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect}
	for _, o := range q.Options {
		if o.Recommended {
			cq.Options = append(cq.Options, o)
		}
	}
	for _, o := range q.Options {
		if !o.Recommended {
			cq.Options = append(cq.Options, o)
		}
	}
	return cq
}

// chatModeWords names a planning mode for the line under the title.
var chatModeWords = map[string]string{
	plansession.ModeNew:       "New plan",
	plansession.ModeExtend:    "Extending the plan",
	plansession.ModeAmend:     "Amending the plan",
	plansession.ModeReconcile: "Reconciling the plan",
}

// chatSub is the line under the title: "New plan · Rounds". A mode afk does
// not know is shown as written, so nothing in the state file is hidden.
func chatSub(mode, phase string) string {
	var parts []string
	if w, ok := chatModeWords[mode]; ok {
		parts = append(parts, w)
	} else if mode != "" {
		parts = append(parts, mode)
	}
	if phase != "" {
		parts = append(parts, capitalise(phase))
	}
	return strings.Join(parts, " · ")
}

// chatSteps places the session's phase among its mode's phases. A phase that
// is not in the list marks nothing, except approved, which comes after every
// phase of a mode whose list stops at review.
func chatSteps(mode, phase string) []chatStep {
	phases := plansession.PhasesFor(mode)
	at := -1
	for i, ph := range phases {
		if ph == phase {
			at = i
		}
	}
	if at < 0 && phase == plansession.PhaseApproved {
		at = len(phases)
	}
	steps := make([]chatStep, 0, len(phases))
	for i, ph := range phases {
		steps = append(steps, chatStep{Name: capitalise(ph), Done: at >= 0 && i < at, Current: i == at})
	}
	return steps
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// chatPath is the chat screen's address, where its forms post too.
func chatPath(project string) string {
	return planBase(project) + "/chat"
}

// chatToastCodes are the toast codes that belong to the chat screen alone.
// toastFor leaves them out so another screen's address cannot show them.
// "started" and "resumed" are not here: other screens use those words for
// their own toasts (a session started, a project resumed), which toastFor
// and sessionToast already keep apart, so the chat reads its own sentences
// from chatToastTexts instead of from the shared table.
var chatToastCodes = map[string]bool{"sent": true}

// chatToastTexts are the chat screen's toasts by code: after a reply, and
// after the change POST started or resumed a planning session (T8.14).
var chatToastTexts = map[string]string{
	"sent":    "Sent to the planning agent.",
	"started": "The planning session started. Its first message appears here when the agent has read the plan.",
	"resumed": "The planning session was already open; here it is.",
}

// chatToast is the toast on the chat screen after a reply was sent or a
// planning session was started.
func chatToast(code string) string {
	return chatToastTexts[code]
}

// readChat asks Deps.PlanChat for the project's planning session, or says in
// the chat's own words that this page cannot read one. Messages and Questions
// are never nil, so they encode as [] and a page script can iterate them
// without a check.
func (s *server) readChat(p Project) planchat.Chat {
	var chat planchat.Chat
	if s.d.PlanChat == nil {
		chat = planchat.Chat{
			Project:   p.Name,
			State:     planchat.StateUnknown,
			Messages:  []planchat.Message{},
			Questions: []provider.Question{},
			Problem:   "This page cannot read planning sessions.",
		}
	} else {
		chat = s.d.PlanChat(p.Name, p.Root)
	}
	if chat.Messages == nil {
		chat.Messages = []planchat.Message{}
	}
	if chat.Questions == nil {
		chat.Questions = []provider.Question{}
	}
	return chat
}

// chatPageView reads what the screen needs besides the chat and lays it out.
// The planning status comes from Deps.PlanStatus when the daemon has one and
// from the state file itself otherwise; both are file reads that cannot
// block. Repository runs git behind its seam, bounded there.
func (s *server) chatPageView(p Project, chat planchat.Chat, in chatPageInput) chatPage {
	in.Project, in.Chat, in.Folder = p.Name, chat, p.Root
	in.HasPlan = s.changeState(p).HasPlan
	if s.d.PlanStatus != nil {
		in.Status = s.planStatusFor(p.Root)
	} else {
		st, ok, err := plansession.ReadState(p.Root)
		in.Status = PlanStatus{Known: true, Session: ok, Damaged: err != nil, Mode: st.Mode, Phase: st.Phase}
	}
	if s.d.Repository != nil {
		in.Repository = s.d.Repository(p.Root)
	}
	return chatPageFor(in)
}

// planChat is GET /api/p/{project}/plan/chat and its page twin
// GET /p/{project}/plan/chat: the project's open planning session as
// messages, its state and any pending question.
//
// Configured projects and projects being planned are answered for
// (findChatProject). A plan waiting for approval that is not being planned
// has no planning session to read; its plan view is the only thing the page
// has for it.
//
// The answer is never cached, page or JSON. The conversation changes under
// the reader as the agent works, and a stale "waiting" would tell the owner
// to reply to a question that has already been answered.
func (s *server) planChat(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findChatProject(w, r)
	if !ok {
		return
	}
	v := ChatView{Chat: s.readChat(p)}
	if !isAPI(r.URL.Path) {
		v.Page = s.chatPageView(p, v.Chat, chatPageInput{})
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respond(w, r, http.StatusOK, "plan_chat", v)
}

// planReplyUnreachable is said when the planning host could not be reached,
// by the API and the page alike.
const planReplyUnreachable = "The planning session could not be reached, so your reply was not sent. Try again, or open the session in afk dash."

// planRefusalBody is the JSON body of a refused reply: the sentence for the
// owner and the reason a page script branches on.
type planRefusalBody struct {
	Error  string                 `json:"error"`
	Reason planchat.RefusalReason `json:"reason"`
}

// refusalStatus is the HTTP status of a refused reply, the same for the API
// and the page. 409 means the session is not in a state to take this reply
// (the owner waits or answers differently); 400 means the reply itself is
// wrong (an option not offered, an empty or malformed reply).
func refusalStatus(reason planchat.RefusalReason) int {
	switch reason {
	case planchat.ReasonNoSession, planchat.ReasonNotWaiting, planchat.ReasonWrongKind, planchat.ReasonUnsupported:
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

// planReply is POST /api/p/{project}/plan/chat: the owner's reply, typed text,
// chosen options or an own answer, handed to the project's open planning
// session.
//
// This handler only translates. Whether the reply may be sent is decided
// behind Deps.PlanReply (by planchat), and it comes back as a *planchat.Refusal
// whose own sentence is shown, with its reason beside it; deciding it here as
// well would let the two drift apart. The sign-in, the Origin check and the
// 64 KB limit are the guard's and are not repeated.
func (s *server) planReply(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findChatProject(w, r)
	if !ok {
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		s.fail(w, r, http.StatusUnsupportedMediaType, "The reply must be sent as JSON with the content type application/json, like {\"text\": \"your reply\"}.")
		return
	}
	var reply planchat.Reply
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reply); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.tooLarge(w, r)
			return
		}
		s.fail(w, r, http.StatusBadRequest, "The reply could not be read. Send JSON like {\"text\": \"your reply\"} or {\"choices\": [[\"an option\"]]}, with nothing else in it.")
		return
	}
	if s.d.PlanReply == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "This page cannot send replies.")
		return
	}
	err := s.d.PlanReply(p.Name, p.Root, reply)
	var refusal *planchat.Refusal
	switch {
	case err == nil:
		sent := "text"
		if reply.Choices != nil {
			sent = "choice"
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"project": p.Name, "sent": sent})
	case errors.As(err, &refusal):
		writeJSON(w, refusalStatus(refusal.Reason), planRefusalBody{Error: refusal.Message, Reason: refusal.Reason})
	default:
		s.fail(w, r, http.StatusBadGateway, planReplyUnreachable)
	}
}

// planChatReply is POST /p/{project}/plan/chat, the chat screen's forms
// without the script: the same reply as the API's, read from form fields,
// sent through the same Deps.PlanReply, and answered as a page.
//
// No confirm is asked for. A reply is not destructive, it is what the agent
// asked for, and a confirm on every message would defeat a chat.
//
// It answers in three ways. A sent reply redirects to the screen with the
// "sent" toast, so reloading the page the owner lands on cannot send it
// again. A refusal draws the screen again, at the API's status, with the
// refusal's own sentence and the owner's typed words back in their box. A
// host that cannot be reached is the error page at 502, as for the API.
func (s *server) planChatReply(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findChatProject(w, r)
	if !ok {
		return
	}
	if s.d.PlanReply == nil {
		s.notFound(w, r)
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
		s.fail(w, r, http.StatusUnsupportedMediaType, "The reply must be sent from the chat screen's form. Open the chat and send it from there.")
		return
	}
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.tooLarge(w, r)
			return
		}
		s.fail(w, r, http.StatusBadRequest, "The reply could not be read. Open the chat and send it again.")
		return
	}
	form := r.PostForm
	pending := 0
	if hasChoiceField(form) {
		pending = len(s.readChat(p).Questions)
	}
	reply := replyFromForm(form, pending)
	err := s.d.PlanReply(p.Name, p.Root, reply)
	var refusal *planchat.Refusal
	switch {
	case err == nil:
		w.Header().Set("Location", chatPath(p.Name)+"?done=sent")
		w.WriteHeader(http.StatusSeeOther)
	case errors.As(err, &refusal):
		v := ChatView{Chat: s.readChat(p)}
		v.Page = s.chatPageView(p, v.Chat, chatPageInput{Error: refusal.Message, Text: form.Get("text"), Other: form.Get("other")})
		w.Header().Set("Cache-Control", "no-store")
		s.renderPage(w, r, refusalStatus(refusal.Reason), "plan_chat", v)
	default:
		s.fail(w, r, http.StatusBadGateway, planReplyUnreachable)
	}
}

// choiceFieldPrefix starts the field of question i's options: choice-0,
// choice-1 and so on, one value per option picked.
const choiceFieldPrefix = "choice-"

func hasChoiceField(form url.Values) bool {
	for key := range form {
		if strings.HasPrefix(key, choiceFieldPrefix) {
			return true
		}
	}
	return false
}

// replyFromForm builds the one planchat.Reply a chat form says. Pure. pending
// is the number of questions the session has open now (Deps.PlanChat), read
// only when the form holds a choice.
//
// The fields are text (the reply box), choice-<i> (question i's options, the
// field repeated for each option of a multi-select question) and other (the
// own-answer box). The reply is shaped here and judged by planchat.Check
// behind Deps.PlanReply, so nothing is refused in this function: a form that
// says too little or too much is passed on as it is, and Check says what is
// wrong with it in its own words.
//
//   - Choices are put in the order of the pending questions, choice-0 first.
//     A question with no field gets an empty list, which Check refuses with
//     a sentence naming that question; a field for a question that is not
//     pending (a page drawn before the agent moved on) adds one more answer
//     than there are questions, which Check refuses as the wrong count.
//   - A typed own answer wins over a chosen option, as typed text beats a
//     picked option in the inbox: the own-answer box sits in the same form as
//     the option buttons, and an owner who typed their own words and then
//     pressed an option out of habit meant the words.
//   - Text is passed along whatever else the form holds, so a form carrying
//     typed text and a choice is refused as two replies at once.
func replyFromForm(form url.Values, pending int) planchat.Reply {
	reply := planchat.Reply{Text: form.Get("text")}
	other := form.Get("other")
	if strings.TrimSpace(other) != "" {
		reply.Other = other
		return reply
	}
	if hasChoiceField(form) {
		choices := make([][]string, pending, pending+1)
		extra := false
		for key, values := range form {
			rest, ok := strings.CutPrefix(key, choiceFieldPrefix)
			if !ok {
				continue
			}
			i, err := strconv.Atoi(rest)
			if err != nil || i < 0 || i >= pending || strconv.Itoa(i) != rest {
				extra = true
				continue
			}
			for _, v := range values {
				if v != "" {
					choices[i] = append(choices[i], v)
				}
			}
		}
		if extra {
			choices = append(choices, nil)
		}
		for i := range choices {
			if choices[i] == nil {
				choices[i] = []string{}
			}
		}
		reply.Choices = choices
		return reply
	}
	// A box holding only spaces is still passed on as an own answer, so
	// Check says it is empty instead of that nothing was sent.
	reply.Other = other
	return reply
}
