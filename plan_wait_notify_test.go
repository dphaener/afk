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
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/planwait"
	"github.com/dphaener/afk/internal/web"
	"github.com/dphaener/afk/internal/webpush"
)

// TestPlanWaitTargets checks the merge of the page's three lists. The
// load-bearing rows are "review-only gets the plan path" (the page 404s the
// chat of a plan that is only waiting for approval, so a chat path there
// would lead nowhere) and the two clash rows (a later list never wins a name
// or a folder, as in internal/web's reviews and beingPlanned, so a record is
// never kept under a folder the page does not mean by that name).
func TestPlanWaitTargets(t *testing.T) {
	p := func(name, root string) web.Project { return web.Project{Name: name, Root: root} }
	tests := []struct {
		name                     string
		configured, reviews, bps []web.Project
		want                     []planWaitTarget
	}{
		{name: "nothing"},
		{name: "configured gets the chat path", configured: []web.Project{p("app", "/c/app/")},
			want: []planWaitTarget{{Name: "app", Root: "/c/app", Path: "/p/app/plan/chat"}}},
		{name: "review-only gets the plan path", reviews: []web.Project{p("draft", "/c/draft")},
			want: []planWaitTarget{{Name: "draft", Root: "/c/draft", Path: "/p/draft/plan"}}},
		{name: "being planned gets the chat path", bps: []web.Project{p("new one", "/c/new")},
			want: []planWaitTarget{{Name: "new one", Root: "/c/new", Path: "/p/new%20one/plan/chat"}}},
		{name: "precedence: configured, reviews, being planned",
			configured: []web.Project{p("a", "/a")}, reviews: []web.Project{p("b", "/b")}, bps: []web.Project{p("c", "/c")},
			want: []planWaitTarget{
				{Name: "a", Root: "/a", Path: "/p/a/plan/chat"},
				{Name: "b", Root: "/b", Path: "/p/b/plan"},
				{Name: "c", Root: "/c", Path: "/p/c/plan/chat"},
			}},
		{name: "review clashing with configured by folder is dropped",
			configured: []web.Project{p("a", "/a")}, reviews: []web.Project{p("other", "/a/")},
			want: []planWaitTarget{{Name: "a", Root: "/a", Path: "/p/a/plan/chat"}}},
		{name: "review clashing with configured by name is dropped",
			configured: []web.Project{p("a", "/a")}, reviews: []web.Project{p("a", "/elsewhere")},
			want: []planWaitTarget{{Name: "a", Root: "/a", Path: "/p/a/plan/chat"}}},
		{name: "being planned clashing with a review is dropped",
			reviews: []web.Project{p("b", "/b")}, bps: []web.Project{p("b", "/new"), p("n", "/b")},
			want: []planWaitTarget{{Name: "b", Root: "/b", Path: "/p/b/plan"}}},
		{name: "an entry with no folder is skipped", configured: []web.Project{p("x", "")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planWaitTargets(tt.configured, tt.reviews, tt.bps)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("planWaitTargets = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// planWaitRig is a fake world for planWaitNotifier: one project whose chat a
// test sets between passes, a fake clock and a recording push target (push
// nil means no device list at all). The state folder is a real temp folder,
// so a "restart" is a fresh notifier over the same folder.
type planWaitRig struct {
	dir  string
	chat planchat.Chat
	push *pushRig
	log  bytes.Buffer
	now  time.Time
}

const planWaitTestRoot = "/code/app"

func (r *planWaitRig) notifier() planWaitNotifier {
	return planWaitNotifier{
		targets: func() []planWaitTarget {
			return []planWaitTarget{{Name: "app", Root: planWaitTestRoot, Path: "/p/app/plan/chat"}}
		},
		chat:     func(project, root string) planchat.Chat { return r.chat },
		stateDir: r.dir,
		push:     r.pushTarget(),
		now:      func() time.Time { return r.now },
		logf:     daemonLogger(&r.log, func() time.Time { return daemonTestNow }),
	}
}

func (r *planWaitRig) pushTarget() pushTarget {
	if r.push == nil {
		return pushTarget{}
	}
	return pushTarget{dir: r.push.dir, send: r.push.send}
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

// TestPlanWaitNotifier walks one project with one signed-up device through a
// sequence of daemon passes. Every step is load-bearing: the first wait is
// pushed once with the chat path; the same wait is not pushed again, not by
// the same notifier and not by a fresh one over the same state folder (the
// restart, the reason the record is a file); an answer then a new wait
// pushes again; a gone session clears the record; a failed push is not
// recorded and so is retried. No log line holds the device's endpoint.
func TestPlanWaitNotifier(t *testing.T) {
	const ep = "https://push.example/secret-endpoint"
	r := &planWaitRig{dir: t.TempDir(), push: newPushRig(t, ep), now: time.Date(2026, 10, 14, 10, 0, 0, 0, time.FixedZone("x", 3600))}
	first := planWaitChat(planchat.StateWaitingForText, "Which database should it use?\nPostgres or SQLite.")
	again := planWaitChat(planchat.StateWaitingForText, "Which database should it use?", "SQLite.", "And which host?")
	ctx := context.Background()
	fail := func(webpush.Subscription) (bool, error) { return false, errors.New("push service down") }

	type step struct {
		name     string
		chat     planchat.Chat
		pushFail bool
		restart  bool
		sends    int // total pushes after this step
		record   bool
		wantKey  planchat.Chat
		logHas   string
		lastBody string
	}
	steps := []step{
		{name: "first wait pushes with the chat path", chat: first, sends: 1, record: true, wantKey: first,
			lastBody: "Which database should it use?"},
		{name: "same wait, next pass, no push", chat: first, sends: 1, record: true, wantKey: first},
		{name: "restart over the same state folder, no push", chat: first, restart: true, sends: 1, record: true, wantKey: first},
		{name: "working keeps the record", chat: planWaitChat(planchat.StateWorking, "x"), sends: 1, record: true, wantKey: first},
		{name: "answered then waiting again pushes", chat: again, sends: 2, record: true, wantKey: again,
			lastBody: "And which host?"},
		{name: "session gone clears the record", chat: planchat.Chat{State: planchat.StateNone}, sends: 2},
		{name: "failing push is not recorded", chat: first, pushFail: true, sends: 3, logHas: "failed, device kept"},
		{name: "failed push is retried on the next pass", chat: first, sends: 4, record: true, wantKey: first},
		{name: "gone again clears", chat: planchat.Chat{State: planchat.StateNone}, sends: 4},
	}
	n := r.notifier()
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			r.chat = s.chat
			r.push.answer = nil
			if s.pushFail {
				r.push.answer = fail
			}
			r.log.Reset()
			if s.restart {
				n = r.notifier()
			}
			n.check(ctx)
			if len(r.push.pays) != s.sends {
				t.Fatalf("pushes = %d, want %d", len(r.push.pays), s.sends)
			}
			if s.lastBody != "" {
				p := r.push.pays[len(r.push.pays)-1]
				if p.Title != "afk: app is waiting for you" || p.URL != "/p/app/plan/chat" || p.Tag != "afk-plan-app" ||
					!strings.HasPrefix(p.Body, s.lastBody) {
					t.Errorf("pushed %+v", p)
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
			if strings.Contains(r.log.String(), "secret-endpoint") {
				t.Errorf("log holds the device's endpoint: %q", r.log.String())
			}
		})
	}
}

// TestPlanWaitNotifierWrites checks that the state file is written only
// when it changed: a pass that sends nothing leaves the file's time alone,
// and with nothing ever recorded no file is created.
func TestPlanWaitNotifierWrites(t *testing.T) {
	r := &planWaitRig{dir: t.TempDir(), push: newPushRig(t, "https://push.example/a"), now: daemonTestNow}
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
// reads no chat and pushes nothing: a stopping daemon must not start a push.
func TestPlanWaitNotifierStopped(t *testing.T) {
	r := &planWaitRig{dir: t.TempDir(), push: newPushRig(t, "https://push.example/a"), now: daemonTestNow}
	r.chat = planWaitChat(planchat.StateWaitingForText, "Ready?")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.notifier().check(ctx)
	if len(r.push.calls) != 0 {
		t.Errorf("pushed %d after the context ended, want 0", len(r.push.calls))
	}
}

// TestPlanWaitPush walks the web push of a planning wait. The load-bearing
// rows are the record rules: a wait is recorded when one device took the
// push, and is NOT recorded when there is no device or every push failed, so
// the next pass tries again (and the retry row proves the same wait is sent
// again once a device works). The one-of-two row pins that one device taking
// it is enough to record, and the gone row that a device the service dropped
// leaves the list.
func TestPlanWaitPush(t *testing.T) {
	wait := planWaitChat(planchat.StateWaitingForText, "Which database should it use?")
	ctx := context.Background()
	epA, epB := "https://push.example/a", "https://push.example/b"
	fail := func(webpush.Subscription) (bool, error) { return false, errors.New("push service down") }

	newRig := func(t *testing.T, eps ...string) *planWaitRig {
		return &planWaitRig{dir: t.TempDir(), chat: wait, push: newPushRig(t, eps...),
			now: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	}
	recorded := func(r *planWaitRig) bool { return len(daemonstate.ReadPlanWaits(r.dir)) == 1 }

	t.Run("one device: pushed once with the chat path and recorded", func(t *testing.T) {
		r := newRig(t, epA)
		n := r.notifier()
		n.check(ctx)
		if len(r.push.calls) != 1 {
			t.Fatalf("pushes=%d, want 1", len(r.push.calls))
		}
		p := r.push.pays[0]
		if p.Title != "afk: app is waiting for you" || p.URL != "/p/app/plan/chat" || p.Tag != "afk-plan-app" ||
			!strings.HasPrefix(p.Body, "Which database should it use?") || r.push.opts[0] != (webpush.Options{}) {
			t.Errorf("payload = %+v opts = %+v", p, r.push.opts[0])
		}
		if !recorded(r) {
			t.Error("the wait was not recorded")
		}
		n.check(ctx)
		if len(r.push.calls) != 1 {
			t.Errorf("same wait next pass pushed again: %d", len(r.push.calls))
		}
	})
	t.Run("review-only project pushes the plan path", func(t *testing.T) {
		r := newRig(t, epA)
		n := r.notifier()
		n.targets = func() []planWaitTarget {
			return []planWaitTarget{{Name: "app", Root: planWaitTestRoot, Path: "/p/app/plan"}}
		}
		n.check(ctx)
		if len(r.push.pays) != 1 || r.push.pays[0].URL != "/p/app/plan" {
			t.Errorf("payloads = %+v", r.push.pays)
		}
	})
	t.Run("no device records nothing", func(t *testing.T) {
		r := newRig(t)
		r.notifier().check(ctx)
		if recorded(r) || len(r.push.calls) != 0 {
			t.Errorf("recorded=%v calls=%d", recorded(r), len(r.push.calls))
		}
	})
	t.Run("every push failing records nothing and is retried", func(t *testing.T) {
		r := newRig(t, epA, epB)
		r.push.answer = fail
		n := r.notifier()
		n.check(ctx)
		if recorded(r) || len(r.push.calls) != 2 {
			t.Fatalf("recorded=%v calls=%d", recorded(r), len(r.push.calls))
		}
		if _, err := os.Stat(daemonstate.PlanWaitsPath(r.dir)); !os.IsNotExist(err) {
			t.Errorf("plan-waits.json exists (stat err %v)", err)
		}
		r.push.answer = nil
		n.check(ctx)
		if len(r.push.calls) != 4 || !recorded(r) {
			t.Errorf("after the retry calls=%d recorded=%v, want 4 and true", len(r.push.calls), recorded(r))
		}
	})
	t.Run("one of two devices sent records", func(t *testing.T) {
		r := newRig(t, epA, epB)
		r.push.answer = func(sub webpush.Subscription) (bool, error) {
			if sub.Endpoint == epB {
				return false, errors.New("down")
			}
			return false, nil
		}
		r.notifier().check(ctx)
		if !recorded(r) {
			t.Error("the wait was not recorded")
		}
	})
	t.Run("gone device is removed and, as the only one, records nothing", func(t *testing.T) {
		r := newRig(t, epA)
		r.push.answer = func(webpush.Subscription) (bool, error) { return true, nil }
		r.notifier().check(ctx)
		if got := webpush.ReadDevices(r.push.dir); len(got) != 0 {
			t.Errorf("devices left = %d, want 0", len(got))
		}
		if recorded(r) {
			t.Error("recorded although no device took the push")
		}
	})
}
