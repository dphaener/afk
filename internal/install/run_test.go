package install

import (
	"bytes"
	"encoding/xml"
	"io"
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

// TestBrewPackages: load-bearing are the order (Facts.Tools order, not
// alphabetical) and the skipped unknown tool, which must not become a guess.
func TestBrewPackages(t *testing.T) {
	unknown := full()
	unknown.Tools = append(unknown.Tools, Tool{Name: "mystery"})
	cases := []struct {
		name string
		f    Facts
		want string
	}{
		{"none missing", full(), ""},
		{"some missing in order", without(without(full(), "ruby"), "gh"), "gh,ruby"},
		{"unknown tool skipped", unknown, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Join(BrewPackages(c.f), ","); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestLaunchAgentPath(t *testing.T) {
	want := "/Users/me/Library/LaunchAgents/io.github.dphaener.afk.plist"
	if got := LaunchAgentPath("/Users/me"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// plistTokens flattens a rendered plist into its element starts and text, failing on
// malformed XML, so a test can check keys and values in order.
func plistTokens(t *testing.T, b []byte) []string {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = true
	var out []string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("not well-formed XML: %v\n%s", err, b)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			out = append(out, "<"+v.Name.Local)
		case xml.CharData:
			if s := strings.TrimSpace(string(v)); s != "" {
				out = append(out, s)
			}
		}
	}
}

// TestRenderLaunchAgent: load-bearing are the escaping rows (a PATH and a log
// path holding & and < must stay well-formed and round-trip), the program
// before "daemon", and the marker on line 2.
func TestRenderLaunchAgent(t *testing.T) {
	plain := LaunchAgent{Program: "/Users/me/bin/afk", PATH: "/Users/me/bin:/usr/bin", LogPath: "/Users/me/.local/state/afk/daemon.log"}
	odd := LaunchAgent{Program: "/Users/me/bin/afk", PATH: "/a&b:/c<d>", LogPath: "/x&y/<log>.log"}
	cases := []struct {
		name string
		a    LaunchAgent
	}{{"plain", plain}, {"escaped", odd}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := RenderLaunchAgent(c.a)
			toks := plistTokens(t, out)
			joined := strings.Join(toks, "\n")
			want := []string{
				"Label\n<string\n" + LaunchAgentLabel,
				"ProgramArguments\n<array\n<string\n" + c.a.Program + "\n<string\ndaemon",
				"RunAtLoad\n<true",
				"KeepAlive\n<true",
				"ThrottleInterval\n<integer\n30",
				"AbandonProcessGroup\n<true",
				"EnvironmentVariables\n<dict\n<key\nPATH\n<string\n" + c.a.PATH,
				"StandardOutPath\n<string\n" + c.a.LogPath,
				"StandardErrorPath\n<string\n" + c.a.LogPath,
			}
			for _, w := range want {
				if !strings.Contains(joined, w) {
					t.Errorf("missing %q in\n%s", w, joined)
				}
			}
			lines := strings.Split(string(out), "\n")
			if lines[0] != `<?xml version="1.0" encoding="UTF-8"?>` || lines[1] != PlistMarker {
				t.Errorf("first lines: %q", lines[:2])
			}
			if !strings.HasSuffix(string(out), "</plist>\n") {
				t.Errorf("no trailing newline")
			}
			if strings.Contains(string(out), "ProcessType") || strings.Contains(string(out), "WorkingDirectory") {
				t.Errorf("extra keys")
			}
		})
	}
	t.Run("raw characters are escaped", func(t *testing.T) {
		out := string(RenderLaunchAgent(odd))
		if strings.Contains(out, "/a&b") || strings.Contains(out, "c<d>") {
			t.Errorf("unescaped value in\n%s", out)
		}
	})
}

// TestLaunchAgentWrittenByAfk: load-bearing are the Apple-style plist without
// the marker (the owner's file, never replaced silently) and marker on line 1
// (the declaration must come first, so that is not ours).
func TestLaunchAgentWrittenByAfk(t *testing.T) {
	decl := `<?xml version="1.0" encoding="UTF-8"?>`
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"rendered", string(RenderLaunchAgent(LaunchAgent{Program: "/p", PATH: "/b", LogPath: "/l"})), true},
		{"apple style without marker", decl + "\n<!DOCTYPE plist>\n<plist/>\n", false},
		{"marker on line 1", PlistMarker + "\n" + decl + "\n", false},
		{"empty", "", false},
		{"declaration only", decl, false},
		{"crlf", decl + "\r\n" + PlistMarker + "\r\n<plist/>\r\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LaunchAgentWrittenByAfk([]byte(c.in)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
