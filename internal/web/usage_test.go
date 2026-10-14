package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
)

// usageRefreshOf reads GET /api/usage's refresh object, failing when it is
// missing.
func usageRefreshOf(t *testing.T, rg *rig) map[string]any {
	t.Helper()
	m := decode(t, rg.do(http.MethodGet, "/api/usage", "", ""))
	r, ok := m["refresh"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/usage has no refresh object: %s", dump(m))
	}
	return r
}

// TestUsageRefresh presses Refresh usage once, over the API and from the
// page, and checks the answer, that the seam ran exactly once, and what the
// usage view says once the reading has finished.
//
// Load-bearing rows:
//   - "a reading": 202 started, then refreshed with the seam's own sentence.
//     It is the whole happy path the button promises.
//   - "a failed reading keeps the old reading": a failure writes no file, so
//     GET must still serve the stored reading, and the refresh object is the
//     only place the failure's sentence lives.
//   - "a panicking reading": without the recover the daemon dies, and
//     without finish in a defer the refresher stays running forever and the
//     button never works again (the second press proves it does).
//   - the page rows: the redirect codes are what the toast reads, and the
//     toast must only ever be one of the fixed sentences.
func TestUsageRefresh(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(rg *rig)
		page   bool
		status int
		// outcome is the API's {"outcome"}; location the page's redirect.
		outcome  string
		location string
		state    string
		text     string
		// check runs after the reading has finished.
		check func(t *testing.T, rg *rig)
	}{
		{
			name:    "a reading",
			status:  http.StatusAccepted,
			outcome: "started",
			state:   UsageRefreshed,
			text:    "Usage read: 41% of the session used.",
		},
		{
			name: "an agent that cannot report usage",
			setup: func(rg *rig) {
				rg.refreshResult = UsageRefreshResult{Outcome: UsageRefreshUnsupported, Text: "This agent cannot report its usage."}
			},
			status:  http.StatusAccepted,
			outcome: "started",
			state:   UsageRefreshUnsupported,
			text:    "This agent cannot report its usage.",
		},
		{
			name: "a failed reading keeps the old reading",
			setup: func(rg *rig) {
				rg.refreshResult = UsageRefreshResult{Outcome: UsageRefreshFailed, Text: "The reading timed out."}
			},
			status:  http.StatusAccepted,
			outcome: "started",
			state:   UsageRefreshFailed,
			text:    "The reading timed out.",
			check: func(t *testing.T, rg *rig) {
				m := decode(t, rg.do(http.MethodGet, "/api/usage", "", ""))
				if m["known"] != true || m["source"] != "probe" || len(m["windows"].([]any)) != 2 {
					t.Errorf("the old reading is not served after a failure: %s", dump(m))
				}
			},
		},
		{
			name:    "a failure with no sentence gets the fallback",
			setup:   func(rg *rig) { rg.refreshResult = UsageRefreshResult{Outcome: UsageRefreshFailed, Text: "  "} },
			status:  http.StatusAccepted,
			outcome: "started",
			state:   UsageRefreshFailed,
			text:    failedFallback,
		},
		{
			name:    "an outcome the page does not know is a failure",
			setup:   func(rg *rig) { rg.refreshResult = UsageRefreshResult{Outcome: "maybe", Text: "Who knows."} },
			status:  http.StatusAccepted,
			outcome: "started",
			state:   UsageRefreshFailed,
			text:    unknownOutcomeText,
		},
		{
			name:    "a panicking reading",
			setup:   func(rg *rig) { rg.refreshPanic = true },
			status:  http.StatusAccepted,
			outcome: "started",
			state:   UsageRefreshFailed,
			text:    refreshPanicText,
			check: func(t *testing.T, rg *rig) {
				if sn := rg.srv.refresher.snapshot(); sn.Running {
					t.Fatalf("the refresher is still running after a panic")
				}
				rg.refreshPanic = false
				if rec := rg.do(http.MethodPost, "/api/usage/refresh", "", ""); rec.Code != http.StatusAccepted {
					t.Errorf("a press after the panic answered %d, want 202", rec.Code)
				}
				rg.waitRefresh()
				if n := rg.refreshCalls.Load(); n != 2 {
					t.Errorf("the seam ran %d times, want 2", n)
				}
				if r := usageRefreshOf(t, rg); r["state"] != UsageRefreshed {
					t.Errorf("after the second press refresh = %s", dump(r))
				}
			},
		},
		{
			name:     "from the page",
			page:     true,
			status:   http.StatusSeeOther,
			location: "/usage?done=refreshing",
			state:    UsageRefreshed,
			text:     "Usage read: 41% of the session used.",
			check: func(t *testing.T, rg *rig) {
				body := rg.pageDo(http.MethodGet, "/usage?done=refreshing", "").Body.String()
				if !strings.Contains(body, "Reading usage. This can take up to a minute.") {
					t.Errorf("the usage page does not show the refreshing toast")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.setup != nil {
				tc.setup(rg)
			}
			if tc.page {
				r := rg.pageDo(http.MethodPost, "/usage/refresh", "")
				if r.Code != tc.status {
					t.Fatalf("status = %d, want %d\n%s", r.Code, tc.status, r.Body.String())
				}
				if loc := r.Header().Get("Location"); loc != tc.location {
					t.Errorf("Location = %q, want %q", loc, tc.location)
				}
			} else {
				r := rg.do(http.MethodPost, "/api/usage/refresh", "", "")
				if r.Code != tc.status {
					t.Fatalf("status = %d, want %d\n%s", r.Code, tc.status, r.Body.String())
				}
				m := decode(t, r)
				if m["outcome"] != tc.outcome || len(m) != 1 {
					t.Errorf("body = %s, want {\"outcome\": %q}", dump(m), tc.outcome)
				}
			}
			rg.waitRefresh()
			if n := rg.refreshCalls.Load(); n != 1 {
				t.Errorf("the seam ran %d times, want 1", n)
			}
			r := usageRefreshOf(t, rg)
			if r["state"] != tc.state || r["text"] != tc.text {
				t.Errorf("refresh = %s, want state %q text %q", dump(r), tc.state, tc.text)
			}
			if r["at"] != "2026-10-12T12:00:00Z" {
				t.Errorf("refresh at = %v, want the finish time", r["at"])
			}
			if tc.check != nil {
				tc.check(t, rg)
			}
		})
	}
}

// TestUsageRefreshSecondPress presses again while a reading runs.
//
// Load-bearing: every row. The guard is the only thing between two presses
// and two runs of the agent's tool racing to write usage.json; the seam
// must run once whichever way the second press comes in, and the view must
// say running while it does.
func TestUsageRefreshSecondPress(t *testing.T) {
	cases := []struct {
		name     string
		page     bool
		status   int
		outcome  string
		location string
	}{
		{name: "over the API", status: http.StatusOK, outcome: "already"},
		{name: "from the page", page: true, status: http.StatusSeeOther, location: "/usage?done=already_refreshing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.blockRefresh()
			if rec := rg.do(http.MethodPost, "/api/usage/refresh", "", ""); rec.Code != http.StatusAccepted {
				t.Fatalf("first press = %d, want 202", rec.Code)
			}
			if r := usageRefreshOf(t, rg); r["state"] != refreshRunning || r["text"] != "" || r["at"] != "2026-10-12T12:00:00Z" {
				t.Errorf("while running refresh = %s", dump(r))
			}
			if tc.page {
				rec := rg.pageDo(http.MethodPost, "/usage/refresh", "")
				if rec.Code != tc.status || rec.Header().Get("Location") != tc.location {
					t.Errorf("second press = %d to %q, want %d to %q", rec.Code, rec.Header().Get("Location"), tc.status, tc.location)
				}
				body := rg.pageDo(http.MethodGet, tc.location, "").Body.String()
				if !strings.Contains(body, "A usage reading is already running.") {
					t.Errorf("the usage page does not show the already-running toast")
				}
			} else {
				rec := rg.do(http.MethodPost, "/api/usage/refresh", "", "")
				if rec.Code != tc.status {
					t.Errorf("second press = %d, want %d", rec.Code, tc.status)
				}
				if m := decode(t, rec); m["outcome"] != tc.outcome {
					t.Errorf("second press body = %s", dump(m))
				}
			}
			rg.releaseRefresh()
			rg.waitRefresh()
			if n := rg.refreshCalls.Load(); n != 1 {
				t.Errorf("the seam ran %d times, want 1", n)
			}
			if r := usageRefreshOf(t, rg); r["state"] != UsageRefreshed {
				t.Errorf("after release refresh = %s", dump(r))
			}
		})
	}
}

// TestUsageRefreshNeverOnRead checks that reading a page never starts a
// reading. Load-bearing: all of them. A reading runs the agent's tool and
// can take a minute; a page that started one on every load (or every live
// re-fetch every two seconds) would run it without end.
func TestUsageRefreshNeverOnRead(t *testing.T) {
	for _, path := range []string{"/usage", "/usage?done=refreshing", "/api/usage", "/", "/api/", "/p/app", "/api/usage/refresh"} {
		t.Run(path, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.pageDo(http.MethodGet, path, "")
			rg.waitRefresh()
			if n := rg.refreshCalls.Load(); n != 0 {
				t.Errorf("GET %s ran a usage reading %d times", path, n)
			}
			if r := usageRefreshOf(t, rg); r["state"] != refreshIdle || r["text"] != "" {
				t.Errorf("refresh = %s, want idle", dump(r))
			}
			if _, ok := usageRefreshOf(t, rg)["at"]; ok {
				t.Errorf("an idle refresh carries a time")
			}
		})
	}
}

// TestUsageRefreshWithoutSeam: a daemon built without RefreshUsage offers no
// refresh. Load-bearing: the 404s (the address must not pretend to start a
// reading nothing will take) and the missing refresh object (the page draws
// no button from it).
func TestUsageRefreshWithoutSeam(t *testing.T) {
	h, err := New(Config{Token: testToken}, Deps{
		LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil },
		Sessions:  func(string) []SessionView { return nil },
		Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
		Pause:     func(string) error { return nil },
		Resume:    func(string) error { return nil },
		Now:       func() time.Time { return testNow },
	})
	must(t, err)
	rg := &rig{t: t, h: h}
	for _, tc := range []struct {
		name string
		path string
		page bool
	}{
		{name: "api", path: "/api/usage/refresh"},
		{name: "page", path: "/usage/refresh", page: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if tc.page {
				rec = rg.pageDo(http.MethodPost, tc.path, "")
			} else {
				rec = rg.do(http.MethodPost, tc.path, "", "")
			}
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
	if m := decode(t, rg.do(http.MethodGet, "/api/usage", "", "")); m["refresh"] != nil {
		t.Errorf("refresh = %v, want it left out", m["refresh"])
	}
}

// TestUsageRefreshOverviewUnchanged: the overview's usage block never
// carries the refresh object, so the front page's API answer is what it was.
func TestUsageRefreshOverviewUnchanged(t *testing.T) {
	rg := newRig(t, Config{})
	rg.do(http.MethodPost, "/api/usage/refresh", "", "")
	rg.waitRefresh()
	u := decode(t, rg.do(http.MethodGet, "/api/", "", ""))["usage"].(map[string]any)
	if _, ok := u["refresh"]; ok {
		t.Errorf("the overview's usage has a refresh object: %s", dump(u))
	}
}

// TestUsageToast checks which screens show a refresh toast. Load-bearing:
// "project page" — toastFor would otherwise show "Reading usage" on a
// project after a hand-typed ?done=refreshing, saying a reading started
// when none did.
func TestUsageToast(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
		not  string
	}{
		{name: "refreshing", path: "/usage?done=refreshing", want: "Reading usage. This can take up to a minute."},
		{name: "already", path: "/usage?done=already_refreshing", want: "A usage reading is already running."},
		{name: "another screen's code", path: "/usage?done=paused", not: "Paused"},
		{name: "unknown code", path: "/usage?done=%3Cb%3E", not: "&lt;b&gt;"},
		{name: "project page", path: "/p/app?done=refreshing", not: "Reading usage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			body := rg.pageDo(http.MethodGet, tc.path, "").Body.String()
			if tc.want != "" && !strings.Contains(body, tc.want) {
				t.Errorf("the page does not show %q", tc.want)
			}
			if tc.not != "" && strings.Contains(body, tc.not) {
				t.Errorf("the page shows %q", tc.not)
			}
		})
	}
}

// TestRefreshViewFor is the pure state-to-view mapping. Load-bearing:
// "running wins over a failure" (a second reading after a failed one must
// show Refreshing, not the old failure) and the just-checked edges (the page
// says "Checked just now" only within a minute of a success).
func TestRefreshViewFor(t *testing.T) {
	ok := UsageRefreshResult{Outcome: UsageRefreshed, Text: "read"}
	bad := UsageRefreshResult{Outcome: UsageRefreshFailed, Text: "broke"}
	cases := []struct {
		name  string
		sn    refreshSnapshot
		state string
		text  string
		at    *time.Time
		just  bool
	}{
		{name: "idle", sn: refreshSnapshot{}, state: refreshIdle},
		{name: "running", sn: refreshSnapshot{Running: true, Started: testNow}, state: refreshRunning, at: &testNow},
		{name: "running wins over a failure", sn: refreshSnapshot{Running: true, Started: testNow, Done: true, Last: bad, LastAt: testNow.Add(-time.Minute)}, state: refreshRunning, at: &testNow},
		{name: "refreshed just now", sn: refreshSnapshot{Done: true, Last: ok, LastAt: testNow.Add(-59 * time.Second)}, state: UsageRefreshed, text: "read", just: true},
		{name: "refreshed a minute ago", sn: refreshSnapshot{Done: true, Last: ok, LastAt: testNow.Add(-time.Minute)}, state: UsageRefreshed, text: "read"},
		{name: "refreshed in the future", sn: refreshSnapshot{Done: true, Last: ok, LastAt: testNow.Add(time.Second)}, state: UsageRefreshed, text: "read"},
		{name: "failed is never just checked", sn: refreshSnapshot{Done: true, Last: bad, LastAt: testNow}, state: UsageRefreshFailed, text: "broke"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := refreshViewFor(tc.sn, testNow)
			if v.State != tc.state || v.Text != tc.text || v.JustChecked != tc.just {
				t.Errorf("view = %+v", *v)
			}
			if tc.at != nil && (v.At == nil || !v.At.Equal(*tc.at)) {
				t.Errorf("at = %v, want %v", v.At, *tc.at)
			}
			if tc.state == refreshIdle && v.At != nil {
				t.Errorf("idle at = %v, want nil", v.At)
			}
		})
	}
}

// TestNormalizeRefresh: an empty sentence is never shown and an unknown
// outcome is never shown as success.
func TestNormalizeRefresh(t *testing.T) {
	cases := []struct {
		name string
		in   UsageRefreshResult
		want UsageRefreshResult
	}{
		{"refreshed keeps its text", UsageRefreshResult{UsageRefreshed, " ok "}, UsageRefreshResult{UsageRefreshed, "ok"}},
		{"refreshed fallback", UsageRefreshResult{UsageRefreshed, ""}, UsageRefreshResult{UsageRefreshed, refreshedFallback}},
		{"unsupported fallback", UsageRefreshResult{UsageRefreshUnsupported, ""}, UsageRefreshResult{UsageRefreshUnsupported, unsupportedFallback}},
		{"failed fallback", UsageRefreshResult{UsageRefreshFailed, ""}, UsageRefreshResult{UsageRefreshFailed, failedFallback}},
		{"unknown outcome", UsageRefreshResult{"", "fine"}, UsageRefreshResult{UsageRefreshFailed, unknownOutcomeText}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeRefresh(tc.in); got != tc.want {
				t.Errorf("normalizeRefresh(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestUsageRefresherGeneration: a start and a finish each move the
// generation the live stream fingerprints. Load-bearing: a failure writes no
// file, so this is the only thing that tells an open page it ended.
func TestUsageRefresherGeneration(t *testing.T) {
	u := newUsageRefresher(func() time.Time { return testNow })
	gate := make(chan struct{})
	g0 := u.generation()
	if !u.start(func() UsageRefreshResult { <-gate; return UsageRefreshResult{Outcome: UsageRefreshFailed} }) {
		t.Fatal("start refused on an idle refresher")
	}
	g1 := u.generation()
	close(gate)
	u.wait()
	g2 := u.generation()
	if g1 == g0 || g2 == g1 {
		t.Errorf("generations %d, %d, %d: want each step to change it", g0, g1, g2)
	}
}

// TestGovernorBadge pins the five stored decisions to their badges and
// words. Load-bearing: "unknown" (a file from a newer afk shows its word as
// stored, never a blank or a wrong badge) and "refuse" (the one that stops
// everything must not look calm).
func TestGovernorBadge(t *testing.T) {
	tests := []struct {
		action string
		want   badgeView
	}{
		{"run", badgeView{"badge--running", "Run"}},
		{"no_launch", badgeView{"badge--held", "Launch nothing new"}},
		{"pause", badgeView{"badge--paused", "Pause"}},
		{"hold", badgeView{"badge--idle", "Hold"}},
		{"refuse", badgeView{"badge--failed", "Refuse"}},
		{"unknown", badgeView{"badge--plain", "unknown"}},
		{"", badgeView{"badge--plain", ""}},
	}
	for _, tt := range tests {
		t.Run("action_"+tt.action, func(t *testing.T) {
			if got := governorBadge(tt.action); got != tt.want {
				t.Errorf("governorBadge(%q) = %+v, want %+v", tt.action, got, tt.want)
			}
		})
	}
}

// TestMeterFor checks the colour state, the rounded and clamped width, and
// the words. Load-bearing: "79.5" (rounds to 80 for the bar but is not warn,
// since the state follows the real figure), "99.6" (warn, bar 100, not full)
// and "over 100" and "negative" (the bar is clamped, the words are not).
func TestMeterFor(t *testing.T) {
	tests := []struct {
		name  string
		w     usagestate.Window
		now   int
		class string
		value string
		label string
	}{
		{"zero", usagestate.Window{Kind: "session"}, 0, "", "0% used", "Session usage"},
		{"79.5", usagestate.Window{Kind: "session", UsedPct: 79.5}, 80, "", "80% used", "Session usage"},
		{"80", usagestate.Window{Kind: "weekly", UsedPct: 80, Scope: "all"}, 80, "meter--warn", "80% used", "Weekly (all) usage"},
		{"99.6", usagestate.Window{Kind: "session", UsedPct: 99.6}, 100, "meter--warn", "100% used", "Session usage"},
		{"100", usagestate.Window{Kind: "session", UsedPct: 100}, 100, "meter--full", "100% used", "Session usage"},
		{"over 100", usagestate.Window{Kind: "session", UsedPct: 130}, 100, "meter--full", "130% used", "Session usage"},
		{"negative", usagestate.Window{Kind: "session", UsedPct: -3}, 0, "", "-3% used", "Session usage"},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			m := meterFor(tt.w)
			if m.Now != tt.now || m.Class != tt.class || m.Value != tt.value || m.Label != tt.label {
				t.Errorf("meterFor = %+v, want now %d class %q value %q label %q", m, tt.now, tt.class, tt.value, tt.label)
			}
		})
	}
}
