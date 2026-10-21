package notify

import (
	"net/url"
)

// ItemNotifies says whether a newly added inbox item should notify the owner.
// It is pure and takes plain values so this package never imports
// internal/inbox. A blocking item always notifies: a parked task is waiting on
// the owner. A non-blocking question or notice never does, because assumed
// answers arrive by the dozen; the other non-blocking kinds (failure, env_var,
// external_action, verification) do, since each asks something of the owner
// even though the task carried on.
func ItemNotifies(kind string, blocking bool) bool {
	if blocking {
		return true
	}
	switch kind {
	case "failure", "env_var", "external_action", "verification":
		return true
	}
	// question, notice and any unknown kind.
	return false
}

// Message is the text of one phone notification: a title line and a body.
// Where it goes, and what it opens when tapped, is the sender's business (a
// web push carries its own path, built with ItemPath, PlanPath or ChatPath).
type Message struct {
	Title string
	Body  string
}

// ItemPath is the path of one inbox item on the web page, with no host. A
// push notification opens a path against the page's own origin, so it needs
// no public address.
func ItemPath(project, id string) string {
	return ProjectPath(project) + "/inbox/" + url.PathEscape(id)
}

// ProjectPath is the path of one project's page, the root of the other paths.
func ProjectPath(project string) string {
	return "/p/" + url.PathEscape(project)
}

// PlanPath is the path of one project's plan view.
func PlanPath(project string) string {
	return ProjectPath(project) + "/plan"
}

// ChatPath is the path of one project's planning chat.
func ChatPath(project string) string {
	return PlanPath(project) + "/chat"
}

// InboxMessage decides whether a newly added inbox item notifies the owner,
// and builds the text. It is pure so the whole "who gets bothered, and with
// what" table is testable without a server. It takes the item's fields rather
// than an inbox.Item so this package does not import internal/inbox.
//
// Which kinds notify is ItemNotifies' table. The message ends with the
// command that shows the item, because a notification is read on a phone and
// acted on at a terminal later.
func InboxMessage(id, task, kind, title, body string, blocking bool) (Message, bool) {
	if !ItemNotifies(kind, blocking) {
		return Message{}, false
	}
	if body != "" {
		body += "\n\n"
	}
	body += "afk inbox show " + id
	if task != "" {
		body += " (" + task + ")"
	}
	return Message{Title: "afk: " + title, Body: body}, true
}
