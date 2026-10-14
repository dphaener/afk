package web

import (
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
