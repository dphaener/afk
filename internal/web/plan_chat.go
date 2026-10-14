package web

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"

	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/provider"
)

// planChat is GET /api/p/{project}/plan/chat: the project's open planning
// session as messages, its state and any pending question.
//
// Only configured projects are answered for (findProject). A plan waiting
// for approval has no planning session to read; its plan view is the only
// thing the page has for it.
//
// The answer is never cached. The conversation changes under the reader as
// the agent works, and a stale "waiting" would tell the owner to reply to a
// question that has already been answered.
func (s *server) planChat(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
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
	// A function that hands back nil slices must still encode as [].
	if chat.Messages == nil {
		chat.Messages = []planchat.Message{}
	}
	if chat.Questions == nil {
		chat.Questions = []provider.Question{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, chat)
}

// planReply is POST /api/p/{project}/plan/chat: the owner's reply, typed text
// or chosen options, handed to the project's open planning session.
//
// This handler only translates. Whether the reply may be sent is decided
// behind Deps.PlanReply (by planchat), and it comes back as a *planchat.Refusal
// whose own sentence is shown; deciding it here as well would let the two
// drift apart. The token, the Origin check and the 64 KB limit are the guard's
// and are not repeated.
func (s *server) planReply(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
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
		status := http.StatusBadRequest
		switch refusal.Reason {
		case planchat.ReasonNoSession, planchat.ReasonNotWaiting, planchat.ReasonWrongKind, planchat.ReasonUnsupported:
			status = http.StatusConflict
		}
		s.fail(w, r, status, refusal.Message)
	default:
		s.fail(w, r, http.StatusBadGateway, "The planning session could not be reached, so your reply was not sent. Try again, or open the session in afk dash.")
	}
}
