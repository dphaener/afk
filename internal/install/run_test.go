package install

import (
	"os"
	"strings"
	"testing"
)

func plan(states ...State) Plan {
	ids := []StepID{StepSafety, StepPackages, StepAgentCLI, StepAgentLogin, StepUnattended, StepPlugin,
		StepGitHubLogin, StepGitIdentity, StepProjectLocation, StepWebPage, StepService, StepCheck}
	var p Plan
	for i, id := range ids {
		st := Done
		if i < len(states) {
			st = states[i]
		}
		p.Steps = append(p.Steps, Step{ID: id, Title: string(id), State: st, Reason: "why " + string(id)})
	}
	return p
}

// TestNext has a row for every outcome. Load-bearing: the cannot-before-to-do
// row (stop, do not run the later to-do) and the cannot-after row (the to-do
// runs first), and only-check-left (finished, never "run the check").
func TestNext(t *testing.T) {
	cases := []struct {
		name string
		p    Plan
		id   StepID
		kind ActionKind
	}{
		{"first to do", plan(Done, ToDo, ToDo), StepPackages, ActionRun},
		{"cannot before first to do", plan(Done, Cannot, ToDo), StepPackages, ActionStop},
		{"cannot after first to do", plan(Done, ToDo, Cannot), StepPackages, ActionRun},
		{"cannot with nothing to do", plan(Done, Done, Done, Done, Cannot), StepUnattended, ActionStop},
		{"all done", plan(), "", ActionFinished},
		{"only check left", plan(Done, Done, Done, Done, Done, Done, Done, Done, Done, Done, Done, ToDo), "", ActionFinished},
		{"everything cannot", plan(Cannot, Cannot, Cannot), StepSafety, ActionStop},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			s, a := Next(tc.p)
			if a.Kind != tc.kind || s.ID != tc.id {
				t.Errorf("Next = %q %q, want %q %q", s.ID, a.Kind, tc.id, tc.kind)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	p := plan(Done, Done, Done, ToDo)
	if err := Verify(p, StepPackages); err != nil {
		t.Errorf("done step: %v", err)
	}
	err := Verify(p, StepAgentLogin)
	if err == nil || !strings.Contains(err.Error(), "still not done") || !strings.Contains(err.Error(), "why agent-login") {
		t.Errorf("not done: %v", err)
	}
	if Verify(p, "nope") == nil {
		t.Error("unknown step should error")
	}
}

func TestAptPackages(t *testing.T) {
	if got := AptPackages(full()); len(got) != 0 {
		t.Errorf("none missing: %v", got)
	}
	f := without(without(full(), "ruby"), "gh")
	if got := strings.Join(AptPackages(f), ","); got != "gh,ruby" {
		t.Errorf("got %q, want gh,ruby", got)
	}
}

// TestServicePATH: load-bearing is the dedup keeping the first position
// (/usr/bin appears for tools and as a default) and the home folders first.
func TestServicePATH(t *testing.T) {
	f := full()
	f.Tools[0].Path = "/opt/x/git"
	f.AgentCLI.Path = "/home/u/.local/bin/agent"
	want := "/home/u/bin:/home/u/.local/bin:/opt/x:/usr/bin:/usr/local/bin:/bin"
	if got := ServicePATH(f, "/home/u"); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := ServicePATH(Facts{}, "/h"); got != "/h/bin:/h/.local/bin:/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("no tools: %q", got)
	}
}

func TestRenderUnit(t *testing.T) {
	tpl, err := os.ReadFile("../../contrib/afkd.service")
	if err != nil {
		t.Fatal(err)
	}
	out := RenderUnit(tpl, "/a:/b")
	lines := strings.Split(string(out), "\n")
	if lines[0] != UnitMarker {
		t.Errorf("first line %q", lines[0])
	}
	text := string(out)
	if !strings.Contains(text, "\nEnvironment=PATH=/a:/b\n") || strings.Contains(text, "#Environment=PATH=") {
		t.Errorf("PATH line not replaced:\n%s", text)
	}
	if !strings.Contains(text, "ExecStart=%h/bin/afk daemon") {
		t.Error("rest of template lost")
	}
	if !WrittenByAfk(out) {
		t.Error("rendered unit should be recognised")
	}
}

func TestWrittenByAfk(t *testing.T) {
	cases := []struct {
		name string
		unit string
		want bool
	}{
		{"marker first", UnitMarker + "\n[Unit]\n", true},
		{"marker with crlf", UnitMarker + "\r\n[Unit]\r\n", true},
		{"owner's file", "[Unit]\nDescription=mine\n", false},
		{"marker not first", "# mine\n" + UnitMarker + "\n", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			if got := WrittenByAfk([]byte(tc.unit)); got != tc.want {
				t.Errorf("got %v", got)
			}
		})
	}
}

// TestWebAddress: load-bearing rows are the unspecified hosts, which must
// become the machine's address only when one is known.
func TestWebAddress(t *testing.T) {
	cases := []struct {
		name, bind, pub, ip, want string
	}{
		{"public url", "127.0.0.1:8080", "https://afk.example.com", "10.0.0.5", "https://afk.example.com"},
		{"loopback", "127.0.0.1:8080", "", "10.0.0.5", "http://127.0.0.1:8080"},
		{"all addresses with ip", "0.0.0.0:8080", "", "10.0.0.5", "http://10.0.0.5:8080"},
		{"all addresses without ip", "0.0.0.0:8080", "", "", "http://0.0.0.0:8080"},
		{"ipv6 any with ip", "[::]:8080", "", "10.0.0.5", "http://10.0.0.5:8080"},
		{"empty host with ip", ":8080", "", "10.0.0.5", "http://10.0.0.5:8080"},
		{"tailscale address", "100.64.0.1:8080", "", "10.0.0.5", "http://100.64.0.1:8080"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			if got := WebAddress(tc.bind, tc.pub, tc.ip); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
