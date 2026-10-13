package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/projectconfig"
)

// TestPlanReviewLink pins the address in the "plan ready" notification.
//
// Load-bearing rows: every row without a link (no config, page off, no public
// address) must be empty, because a link to a page that is not there is worse
// than none; "a configured project" must use the configured name, not the
// folder's, since that is what the page serves the plan under; "a review-only
// project" must use the name the review list gave.
func TestPlanReviewLink(t *testing.T) {
	const root = "/code/app"
	same := func(configured, r string) bool { return configured == r }
	web := daemonconfig.Web{Token: "secret-token", PublicURL: "https://afk.example"}
	tests := []struct {
		name   string
		cfg    daemonconfig.Config
		err    error
		review string
		want   string
	}{
		{name: "no config", err: errors.New("unreadable"), review: "app"},
		{name: "page off", cfg: daemonconfig.Config{Web: daemonconfig.Web{PublicURL: "https://afk.example"}}, review: "app"},
		{name: "no public address", cfg: daemonconfig.Config{Web: daemonconfig.Web{Token: "secret-token"}}, review: "app"},
		{
			name:   "a configured project",
			cfg:    daemonconfig.Config{Web: web, Projects: []daemonconfig.Project{{Name: "other", Path: "/code/other"}, {Name: "shop", Path: root}}},
			review: "app",
			want:   "https://afk.example/p/shop/plan",
		},
		{name: "a review-only project", cfg: daemonconfig.Config{Web: web}, review: "app-2", want: "https://afk.example/p/app-2/plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planReviewLink(tt.cfg, tt.err, root, tt.review, same)
			if got != tt.want {
				t.Errorf("link = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "secret-token") {
				t.Errorf("link holds the token: %q", got)
			}
		})
	}
}

// reviewRig records what a plan advance would have sent.
type reviewRig struct {
	state   string
	sent    []notify.NtfyMessage
	urls    []string
	sendErr error
	machine daemonconfig.Config
}

func (r *reviewRig) deps() planReviewDeps {
	return planReviewDeps{
		stateDir:     func() (string, error) { return r.state, nil },
		loadProject:  projectconfig.Load,
		loadMachine:  func() (daemonconfig.Config, error) { return r.machine, nil },
		gitCommonDir: func(string) string { return "" },
		send: func(_ context.Context, url string, m notify.NtfyMessage) error {
			r.urls = append(r.urls, url)
			r.sent = append(r.sent, m)
			return r.sendErr
		},
	}
}

// seedReview makes a new-mode session at the check phase whose plan the
// checker passes, with an afk.toml that sets the notification address.
func seedReview(t *testing.T, dir, mode, ntfy string) string {
	t.Helper()
	base := planFixture(t, "base-plan.md")
	seedPlanSession(t, dir, plansession.PhaseCheck, "no: a command line", base)
	if mode != plansession.ModeNew {
		writePlanFile(t, dir, ".afk/plan/state.json", `{"mode":"`+mode+`","phase":"check","updated_at":"2026-10-12T00:00:00Z"}`)
	}
	if ntfy != "" {
		writePlanFile(t, dir, "afk.toml", "[notify]\nntfy_url = \""+ntfy+"\"\n")
	}
	return base
}

// TestPlanReviewNotification pins when `afk plan advance` tells the owner.
//
// Load-bearing rows: "new reaches review" (one send, the title, the checker's
// counts, the link ending /plan, one review entry); "failing sender" (exit 0
// and the phase still review); "change session" and "other phase" (nothing
// sent, nothing listed); "no ntfy address" (nothing sent, nothing printed);
// "page off" (empty link).
func TestPlanReviewNotification(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		ntfy      string
		machine   daemonconfig.Config
		phase     string // phase to seed; "" means check
		sendErr   error
		wantSends int
		wantLink  string // "" with wantSends>0 means an empty link
		wantList  int
		wantErr   string
	}{
		{
			name: "new reaches review", mode: "new", ntfy: "https://ntfy.example/t",
			machine:   daemonconfig.Config{Web: daemonconfig.Web{Token: "tok", PublicURL: "https://afk.example"}},
			wantSends: 1, wantLink: "/plan", wantList: 1,
		},
		{name: "page off", mode: "new", ntfy: "https://ntfy.example/t", wantSends: 1, wantList: 1},
		{
			name: "failing sender", mode: "new", ntfy: "https://ntfy.example/t", sendErr: errors.New("connection refused"),
			wantSends: 1, wantList: 1, wantErr: `afk plan advance: could not send the "plan ready" notification: connection refused`,
		},
		{name: "no ntfy address", mode: "new", wantList: 1},
		{name: "change session", mode: "extend", ntfy: "https://ntfy.example/t"},
		{name: "amend session", mode: "amend", ntfy: "https://ntfy.example/t"},
		{name: "other phase", mode: "new", ntfy: "https://ntfy.example/t", phase: plansession.PhaseBrief},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			plan := seedReview(t, dir, tt.mode, tt.ntfy)
			if tt.phase != "" {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(tt.phase))
			}
			rig := &reviewRig{state: t.TempDir(), sendErr: tt.sendErr, machine: tt.machine}
			f := &planFake{}
			deps := realLintDeps(f, dir)
			deps.review = rig.deps()
			var so, se strings.Builder
			code := runPlanAdvance(nil, &so, &se, deps)
			if tt.mode != plansession.ModeNew {
				// Change modes may refuse to leave check for reasons of their
				// own; what matters is that nothing was sent.
				if len(rig.sent) != 0 {
					t.Fatalf("a %s session sent %d notifications", tt.mode, len(rig.sent))
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, so.String(), se.String())
			}
			if len(rig.sent) != tt.wantSends {
				t.Fatalf("sends = %d, want %d", len(rig.sent), tt.wantSends)
			}
			if got := len(daemonstate.ReadReviews(rig.state)); got != tt.wantList {
				t.Errorf("review entries = %d, want %d", got, tt.wantList)
			}
			if tt.wantErr == "" && strings.Contains(se.String(), "notification") {
				t.Errorf("stderr mentions the notification: %s", se.String())
			}
			if tt.wantErr != "" && !strings.Contains(se.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", se.String(), tt.wantErr)
			}
			if tt.wantSends == 0 {
				return
			}
			m := rig.sent[0]
			if want := "Plan ready for review: " + filepath.Base(dir); m.Title != want {
				t.Errorf("title = %q, want %q", m.Title, want)
			}
			s := planlint.Lint([]byte(plan)).Summary
			if want := planReviewBody(&s); m.Body != want || !strings.HasSuffix(m.Body, " Read it, then run afk plan approve in the project folder.") || !strings.Contains(m.Body, "task") {
				t.Errorf("body = %q, want %q", m.Body, want)
			}
			if rig.urls[0] != tt.ntfy {
				t.Errorf("sent to %q, want %q", rig.urls[0], tt.ntfy)
			}
			if tt.wantLink == "" && m.Click != "" {
				t.Errorf("link = %q, want none", m.Click)
			}
			if tt.wantLink != "" && !strings.HasSuffix(m.Click, tt.wantLink) {
				t.Errorf("link = %q, want it to end %q", m.Click, tt.wantLink)
			}
			if all := m.Click + m.Body + m.Title; strings.Contains(all, "tok") && tt.machine.Web.Token != "" && strings.Contains(all, tt.machine.Web.Token) {
				t.Errorf("the token is in the message: %q", all)
			}
			st, ok, err := plansession.ReadState(dir)
			if err != nil || !ok || st.Phase != plansession.PhaseReview {
				t.Errorf("state = %+v ok=%v err=%v, want phase review", st, ok, err)
			}
		})
	}
}
