package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/planwait"
	"github.com/dphaener/afk/internal/web"
)

// TestPlanWaitTargets checks the merge of the page's three lists. The
// load-bearing rows are "review-only gets the plan link" (the page 404s the
// chat of a plan that is only waiting for approval, so a chat link there
// would lead nowhere) and the two clash rows (a later list never wins a name
// or a folder, as in internal/web's reviews and beingPlanned, so a record is
// never kept under a folder the page does not mean by that name). Every row
// also checks that no link holds a query, a fragment or the token.
func TestPlanWaitTargets(t *testing.T) {
	const public = "https://afk.example/"
	const token = "s3cret-token"
	p := func(name, root string) web.Project { return web.Project{Name: name, Root: root} }
	tests := []struct {
		name                     string
		configured, reviews, bps []web.Project
		public                   string
		want                     []planWaitTarget
	}{
		{name: "nothing", public: public},
		{name: "configured gets the chat link", configured: []web.Project{p("app", "/c/app/")}, public: public,
			want: []planWaitTarget{{Name: "app", Root: "/c/app", Link: "https://afk.example/p/app/plan/chat"}}},
		{name: "review-only gets the plan link", reviews: []web.Project{p("draft", "/c/draft")}, public: public,
			want: []planWaitTarget{{Name: "draft", Root: "/c/draft", Link: "https://afk.example/p/draft/plan"}}},
		{name: "being planned gets the chat link", bps: []web.Project{p("new one", "/c/new")}, public: public,
			want: []planWaitTarget{{Name: "new one", Root: "/c/new", Link: "https://afk.example/p/new%20one/plan/chat"}}},
		{name: "precedence: configured, reviews, being planned", public: public,
			configured: []web.Project{p("a", "/a")}, reviews: []web.Project{p("b", "/b")}, bps: []web.Project{p("c", "/c")},
			want: []planWaitTarget{
				{Name: "a", Root: "/a", Link: "https://afk.example/p/a/plan/chat"},
				{Name: "b", Root: "/b", Link: "https://afk.example/p/b/plan"},
				{Name: "c", Root: "/c", Link: "https://afk.example/p/c/plan/chat"},
			}},
		{name: "review clashing with configured by folder is dropped", public: public,
			configured: []web.Project{p("a", "/a")}, reviews: []web.Project{p("other", "/a/")},
			want: []planWaitTarget{{Name: "a", Root: "/a", Link: "https://afk.example/p/a/plan/chat"}}},
		{name: "review clashing with configured by name is dropped", public: public,
			configured: []web.Project{p("a", "/a")}, reviews: []web.Project{p("a", "/elsewhere")},
			want: []planWaitTarget{{Name: "a", Root: "/a", Link: "https://afk.example/p/a/plan/chat"}}},
		{name: "being planned clashing with a review is dropped", public: public,
			reviews: []web.Project{p("b", "/b")}, bps: []web.Project{p("b", "/new"), p("n", "/b")},
			want: []planWaitTarget{{Name: "b", Root: "/b", Link: "https://afk.example/p/b/plan"}}},
		{name: "no public address, no link",
			configured: []web.Project{p("a", "/a")}, reviews: []web.Project{p("b", "/b")}, bps: []web.Project{p("c", "/c")},
			want: []planWaitTarget{{Name: "a", Root: "/a"}, {Name: "b", Root: "/b"}, {Name: "c", Root: "/c"}}},
		{name: "an entry with no folder is skipped", public: public, configured: []web.Project{p("x", "")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planWaitTargets(tt.configured, tt.reviews, tt.bps, tt.public)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("planWaitTargets = %+v, want %+v", got, tt.want)
			}
			for _, g := range got {
				if strings.ContainsAny(g.Link, "?#") || strings.Contains(g.Link, token) {
					t.Errorf("link %q holds a query, fragment or the token", g.Link)
				}
			}
		})
	}
}

// planWaitRig is a fake world for planWaitNotifier: one project whose chat,
// ntfy address and send result a test sets between passes, a fake clock and
// a recording sender. The state folder is a real temp folder, so a "restart"
// is a fresh notifier over the same folder.
type planWaitRig struct {
	dir     string
	chat    planchat.Chat
	address string
	sendErr error
	sent    []notify.NtfyMessage
	sentTo  []string
	log     bytes.Buffer
	now     time.Time
}

const planWaitTestRoot = "/code/app"

func (r *planWaitRig) notifier() planWaitNotifier {
	return planWaitNotifier{
		targets: func() []planWaitTarget {
			return []planWaitTarget{{Name: "app", Root: planWaitTestRoot, Link: "https://afk.example/p/app/plan/chat"}}
		},
		chat:    func(project, root string) planchat.Chat { return r.chat },
		address: func(string) string { return r.address },
		send: func(ctx context.Context, url string, m notify.NtfyMessage) error {
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("send was given no deadline")
			}
			r.sentTo = append(r.sentTo, url)
			r.sent = append(r.sent, m)
			return r.sendErr
		},
		stateDir: r.dir,
		now:      func() time.Time { return r.now },
		logf:     daemonLogger(&r.log, func() time.Time { return daemonTestNow }),
	}
}

func planWaitChat(state planchat.State, texts ...string) planchat.Chat {
	c := planchat.Chat{Project: "app", State: state, Messages: []planchat.Message{}, Questions: nil}
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for i, tx := range texts {
		who := "agent"
		if i%2 == 1 {
			who = "owner"
		}
		c.Messages = append(c.Messages, planchat.Message{Who: who, Text: tx, At: at.Add(time.Duration(i) * time.Minute)})
	}
	return c
}

// TestPlanWaitNotifier walks one project through a sequence of daemon
// passes. Every step is load-bearing: the first wait sends once with the chat
// link; the same wait is not sent again, not by the same notifier and not by
// a fresh one over the same state folder (the restart, the reason the record
// is a file); an answer then a new wait sends again; a gone session clears
// the record; a failed send is not recorded and so is retried; no ntfy
// address sends and records nothing.
func TestPlanWaitNotifier(t *testing.T) {
	const ntfy = "https://user:pw@ntfy.example/topic"
	r := &planWaitRig{dir: t.TempDir(), address: ntfy, now: time.Date(2026, 10, 14, 10, 0, 0, 0, time.FixedZone("x", 3600))}
	first := planWaitChat(planchat.StateWaitingForText, "Which database should it use?\nPostgres or SQLite.")
	again := planWaitChat(planchat.StateWaitingForText, "Which database should it use?", "SQLite.", "And which host?")
	ctx := context.Background()

	type step struct {
		name     string
		chat     planchat.Chat
		address  string
		sendErr  error
		restart  bool
		sends    int // total sends after this step
		record   bool
		wantKey  planchat.Chat
		logHas   string
		lastBody string
	}
	steps := []step{
		{name: "first wait sends with the chat link", chat: first, address: ntfy, sends: 1, record: true, wantKey: first,
			lastBody: "Which database should it use?"},
		{name: "same wait, next pass, no send", chat: first, address: ntfy, sends: 1, record: true, wantKey: first},
		{name: "restart over the same state folder, no send", chat: first, address: ntfy, restart: true, sends: 1, record: true, wantKey: first},
		{name: "working keeps the record", chat: planWaitChat(planchat.StateWorking, "x"), address: ntfy, sends: 1, record: true, wantKey: first},
		{name: "answered then waiting again sends", chat: again, address: ntfy, sends: 2, record: true, wantKey: again,
			lastBody: "And which host?"},
		{name: "session gone clears the record", chat: planchat.Chat{State: planchat.StateNone}, address: ntfy, sends: 2},
		{name: "failing send is not recorded", chat: first, address: ntfy, sendErr: errors.New("could not reach https://user:xxxxx@ntfy.example/topic: refused"),
			sends: 3, logHas: "could not be sent; it is tried again on the next pass"},
		{name: "failed send is retried on the next pass", chat: first, address: ntfy, sends: 4, record: true, wantKey: first},
		{name: "gone again clears", chat: planchat.Chat{State: planchat.StateNone}, address: ntfy, sends: 4},
		{name: "no ntfy address sends and records nothing", chat: first, address: "", sends: 4},
	}
	n := r.notifier()
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			r.chat, r.address, r.sendErr = s.chat, s.address, s.sendErr
			r.log.Reset()
			if s.restart {
				n = r.notifier()
			}
			n.check(ctx)
			if len(r.sent) != s.sends {
				t.Fatalf("sends = %d, want %d", len(r.sent), s.sends)
			}
			if s.lastBody != "" {
				m := r.sent[len(r.sent)-1]
				if m.Title != "afk: app is waiting for you" || m.Priority != 4 || m.Click != "https://afk.example/p/app/plan/chat" ||
					!strings.HasPrefix(m.Body, s.lastBody) || r.sentTo[len(r.sentTo)-1] != ntfy {
					t.Errorf("sent %+v to %q", m, r.sentTo[len(r.sentTo)-1])
				}
			}
			waits := daemonstate.ReadPlanWaits(r.dir)
			if !s.record {
				if len(waits) != 0 {
					t.Errorf("record = %+v, want none", waits)
				}
			} else {
				d := planwait.Key(s.wantKey)
				if len(waits) != 1 || waits[0].Root != planWaitTestRoot || waits[0].Key != d || waits[0].At != "2026-10-14T09:00:00Z" {
					t.Errorf("record = %+v, want one for %s with key %q at 09:00Z", waits, planWaitTestRoot, d)
				}
			}
			if s.logHas != "" && !strings.Contains(r.log.String(), s.logHas) {
				t.Errorf("log = %q, want it to say %q", r.log.String(), s.logHas)
			}
			if strings.Contains(r.log.String(), "user:pw") {
				t.Errorf("log holds the ntfy credentials: %q", r.log.String())
			}
		})
	}
}

// TestPlanWaitNotifierWrites checks that the state file is written only
// when it changed: a pass that sends nothing leaves the file's time alone,
// and with nothing ever recorded no file is created.
func TestPlanWaitNotifierWrites(t *testing.T) {
	r := &planWaitRig{dir: t.TempDir(), address: "https://ntfy.example/t", now: daemonTestNow}
	r.chat = planWaitChat(planchat.StateWorking, "x")
	n := r.notifier()
	n.check(context.Background())
	if _, err := os.Stat(daemonstate.PlanWaitsPath(r.dir)); !os.IsNotExist(err) {
		t.Fatalf("a pass with nothing to record wrote the file (stat err %v)", err)
	}
	r.chat = planWaitChat(planchat.StateWaitingForText, "Ready?")
	n.check(context.Background())
	before, err := os.Stat(daemonstate.PlanWaitsPath(r.dir))
	if err != nil {
		t.Fatal(err)
	}
	old := before.ModTime().Add(-time.Hour)
	if err := os.Chtimes(daemonstate.PlanWaitsPath(r.dir), old, old); err != nil {
		t.Fatal(err)
	}
	n.check(context.Background())
	after, err := os.Stat(daemonstate.PlanWaitsPath(r.dir))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(old) {
		t.Error("a pass that changed nothing rewrote plan-waits.json")
	}
}

// TestPlanWaitNotifierStopped checks that a pass whose context has ended
// reads no chat and sends nothing: a stopping daemon must not start a send.
func TestPlanWaitNotifierStopped(t *testing.T) {
	r := &planWaitRig{dir: t.TempDir(), address: "https://ntfy.example/t", now: daemonTestNow}
	r.chat = planWaitChat(planchat.StateWaitingForText, "Ready?")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.notifier().check(ctx)
	if len(r.sent) != 0 {
		t.Errorf("sent %d after the context ended, want 0", len(r.sent))
	}
}
