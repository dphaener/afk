package web

import (
	"net/http"
	"strings"
	"testing"
)

// TestBeingPlannedFinder pins which source wins when a name is in more than
// one: configured, then being planned, with a review's name never reaching a being-planned entry. Load-bearing rows: the
// collisions ("configured wins", "review wins"), because a being-planned
// entry that shadowed a configured project would open another folder's chat
// under the project's own address, and the unknown row, because the new
// source must not make every name resolve.
func TestBeingPlannedFinder(t *testing.T) {
	tests := []struct {
		name       string
		planned    []BeingPlannedView
		path       string
		wantStatus int
		wantRoot   func(rg *rig) string
	}{
		{name: "configured wins over a being-planned entry of the same name",
			planned: []BeingPlannedView{{Name: "app", Root: "/elsewhere/app"}}, path: "/api/p/app/plan/chat",
			wantStatus: http.StatusOK, wantRoot: func(rg *rig) string { return rg.root }},
		{name: "an entry on a configured project's folder is dropped, so its own name is a 404",
			planned: []BeingPlannedView{{Name: "other", Root: "ROOT"}}, path: "/api/p/other/plan/chat",
			wantStatus: http.StatusNotFound},
		{name: "review wins over a being-planned entry of the same name, and has no chat",
			planned: []BeingPlannedView{{Name: "draft", Root: "/elsewhere/draft"}}, path: "/api/p/draft/plan/chat",
			wantStatus: http.StatusNotFound},
		{name: "being planned resolves",
			planned: []BeingPlannedView{{Name: "shop", Root: "/code/shop"}}, path: "/api/p/shop/plan/chat",
			wantStatus: http.StatusOK, wantRoot: func(*rig) string { return "/code/shop" }},
		{name: "unknown name is a 404",
			planned: []BeingPlannedView{{Name: "shop", Root: "/code/shop"}}, path: "/api/p/nothing/plan/chat",
			wantStatus: http.StatusNotFound},
		{name: "no list at all is a 404",
			path: "/api/p/shop/plan/chat", wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.beingPlanned = tc.planned
			for i := range rg.beingPlanned {
				if rg.beingPlanned[i].Root == "ROOT" {
					rg.beingPlanned[i].Root = rg.root
				}
			}
			rec := rg.do(http.MethodGet, tc.path, "", "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantRoot == nil {
				if len(rg.chatCalls) != 0 {
					t.Errorf("chat was read for a refused name: %v", rg.chatCalls)
				}
				return
			}
			if len(rg.chatCalls) != 1 || rg.chatCalls[0][1] != tc.wantRoot(rg) {
				t.Errorf("chat read = %v, want root %q", rg.chatCalls, tc.wantRoot(rg))
			}
		})
	}
}

// TestBeingPlannedChatRoutes: the chat page, its API twin and the reply are
// reachable for a being-planned name, while the routes that need a project
// (its page, the change form) still answer 404 for it. The second half is the
// load-bearing part: a plan still being written has nothing to change.
func TestBeingPlannedChatRoutes(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		ctype      string
		body       string
		wantStatus int
	}{
		{name: "chat page", method: http.MethodGet, path: "/p/shop/plan/chat", wantStatus: http.StatusOK},
		{name: "chat API", method: http.MethodGet, path: "/api/p/shop/plan/chat", wantStatus: http.StatusOK},
		{name: "reply API", method: http.MethodPost, path: "/api/p/shop/plan/chat", ctype: "application/json", body: `{"text":"hello"}`, wantStatus: http.StatusOK},
		{name: "project page stays 404", method: http.MethodGet, path: "/p/shop", wantStatus: http.StatusNotFound},
		{name: "project API stays 404", method: http.MethodGet, path: "/api/p/shop", wantStatus: http.StatusNotFound},
		{name: "change form stays 404", method: http.MethodGet, path: "/p/shop/plan/change", wantStatus: http.StatusNotFound},
		{name: "plan view stays 404", method: http.MethodGet, path: "/p/shop/plan", wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.beingPlanned = []BeingPlannedView{{Name: "shop", Root: "/code/shop"}}
			rec := rg.do(tc.method, tc.path, tc.ctype, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestBeingPlannedChatPageHasNoChangeRows: the chat of a project being
// planned offers no Extend, Amend or Reconcile. Without a plan there is
// nothing to change, and the rows would lead to the change form, which
// answers 404 for the project.
func TestBeingPlannedChatPageHasNoChangeRows(t *testing.T) {
	rg := newRig(t, Config{})
	rg.beingPlanned = []BeingPlannedView{{Name: "shop", Root: "/code/shop"}}
	rec := rg.do(http.MethodGet, "/p/shop/plan/chat", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "plan/change?mode=") {
		t.Errorf("chat page offers change rows for a project with no plan:\n%s", rec.Body.String())
	}
}

// TestBeingPlannedOverview covers the page and GET /api/. Load-bearing: the
// duplicate rows (a being-planned entry that a configured project or a review
// already covers is shown once, as that project) and the always-an-array
// field.
func TestBeingPlannedOverview(t *testing.T) {
	tests := []struct {
		name         string
		planned      []BeingPlannedView
		wantRow      bool
		wantRowCount int
		wantAPI      int
	}{
		{name: "one entry shows a row with the badge and the chat link", planned: []BeingPlannedView{{Name: "shop", Root: "/code/shop"}}, wantRow: true, wantAPI: 1},
		{name: "none shows no row", wantAPI: 0},
		{name: "same name as a configured project is not shown twice", planned: []BeingPlannedView{{Name: "app", Root: "/elsewhere/app"}}, wantAPI: 0},
		{name: "same name as a review is not shown twice", planned: []BeingPlannedView{{Name: "draft", Root: "/elsewhere/draft"}}, wantAPI: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.beingPlanned = tc.planned
			page := rg.do(http.MethodGet, "/", "", "")
			if page.Code != http.StatusOK {
				t.Fatalf("page status = %d", page.Code)
			}
			body := page.Body.String()
			has := strings.Contains(body, "Being planned") && strings.Contains(body, `href="/p/shop/plan/chat"`)
			if has != tc.wantRow {
				t.Errorf("row present = %v, want %v\n%s", has, tc.wantRow, body)
			}
			if tc.wantRow {
				for _, want := range []string{`badge--idle">Being planned`, "A new plan is being written. Open the chat to answer it."} {
					if !strings.Contains(body, want) {
						t.Errorf("page lacks %q", want)
					}
				}
			}
			if n := strings.Count(body, "Being planned"); !tc.wantRow && n != 0 {
				t.Errorf("Being planned appears %d times, want 0", n)
			}
			m := decode(t, rg.do(http.MethodGet, "/api/", "", ""))
			got, ok := m["being_planned"].([]any)
			if !ok || len(got) != tc.wantAPI {
				t.Fatalf("being_planned = %#v, want an array of %d", m["being_planned"], tc.wantAPI)
			}
			if tc.wantAPI == 1 {
				e := got[0].(map[string]any)
				if e["name"] != "shop" || e["root"] != "/code/shop" {
					t.Errorf("entry = %v", e)
				}
			}
		})
	}
}

// TestOverviewPageForBeingPlanned: the pure layout. Load-bearing: a planned
// project is an everything-else row (it does not need the owner until the
// plan exists) placed after the project rows, and a name a row or a review
// has is skipped.
func TestOverviewPageForBeingPlanned(t *testing.T) {
	run := RowView{Name: "run", State: "running"}
	tests := []struct {
		name    string
		rows    []RowView
		reviews []ReviewView
		planned []BeingPlannedView
		needs   []string
		others  []string
		heading string
	}{
		{name: "alone", planned: []BeingPlannedView{{Name: "shop"}}, others: []string{"shop"}, heading: "Nothing needs you"},
		{name: "after the rows", rows: []RowView{run}, planned: []BeingPlannedView{{Name: "shop"}}, others: []string{"run", "shop"}, heading: "Nothing needs you"},
		{name: "review stays needs-you", reviews: []ReviewView{{Name: "draft"}}, planned: []BeingPlannedView{{Name: "shop"}}, needs: []string{"draft"}, others: []string{"shop"}, heading: "1 project needs you"},
		{name: "named by a row", rows: []RowView{run}, planned: []BeingPlannedView{{Name: "run"}}, others: []string{"run"}, heading: "Nothing needs you"},
		{name: "named by a review", reviews: []ReviewView{{Name: "draft"}}, planned: []BeingPlannedView{{Name: "draft"}}, needs: []string{"draft"}, heading: "1 project needs you"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := overviewPageFor(tc.rows, tc.reviews, tc.planned...)
			if p.Heading != tc.heading {
				t.Errorf("heading = %q, want %q", p.Heading, tc.heading)
			}
			join := func(rs []overviewRow) string {
				var n []string
				for _, r := range rs {
					n = append(n, r.Name)
				}
				return strings.Join(n, ",")
			}
			if got := join(p.NeedsYou); got != strings.Join(tc.needs, ",") {
				t.Errorf("needs you = %q, want %v", got, tc.needs)
			}
			if got := join(p.Others); got != strings.Join(tc.others, ",") {
				t.Errorf("others = %q, want %v", got, tc.others)
			}
			for _, r := range p.Others {
				if r.Name == "shop" && (r.Href != "/p/shop/plan/chat" || r.Badge != beingPlannedBadge || r.Live) {
					t.Errorf("shop row = %+v", r)
				}
			}
		})
	}
}

// TestWithBeingPlanned: the live stream reads the planning part of a project
// being planned (its chat is live) but never replaces a configured project
// of the same name.
func TestWithBeingPlanned(t *testing.T) {
	conf := []Project{{Name: "app", Root: "/a"}}
	got := withBeingPlanned(conf, []BeingPlannedView{{Name: "app", Root: "/x"}, {Name: "shop", Root: "/s"}})
	if len(got) != 2 || got[0].Root != "/a" || got[1].Name != "shop" || got[1].Root != "/s" {
		t.Errorf("got %v", got)
	}
	if len(conf) != 1 {
		t.Errorf("the configured list was changed: %v", conf)
	}
}
