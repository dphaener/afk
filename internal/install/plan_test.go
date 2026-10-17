package install

import (
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/provider"
)

// full is a fully installed Linux VM; each row changes the fields it cares about.
func full() Facts {
	return Facts{
		OS: "linux", Distro: "ubuntu", DistroVersion: "24.04", Arch: "amd64",
		Virt: VirtVM, PackageManager: "apt-get",
		Tools: []Tool{
			{Name: "git", Found: true, Path: "/usr/bin/git"},
			{Name: "gh", Found: true, Path: "/usr/bin/gh"},
			{Name: "jq", Found: true, Path: "/usr/bin/jq"},
			{Name: "ruby", Found: true, Path: "/usr/bin/ruby"},
		},
		AgentCLI:   Tool{Name: "agent", Found: true, Path: "/usr/bin/agent"},
		AgentLogin: Yes, Unattended: provider.UnattendedSet, AgentPlugin: Yes,
		BinOnPath: true, BinHasAfk: true, BinIsThis: true,
		GitHubLogin: Yes, GitUsesGitHub: Yes, GitName: "A", GitEmail: "a@b.c",
		ProjectLocation: "/home/u/code", ProjectLocationSet: true,
		WebPassword: Yes,
		Service:     ServiceFacts{Installed: Yes, Enabled: Yes, Running: Yes, Linger: Yes},
	}
}

func without(f Facts, name string) Facts {
	ts := append([]Tool(nil), f.Tools...)
	for i := range ts {
		if ts[i].Name == name {
			ts[i].Found = false
		}
	}
	f.Tools = ts
	return f
}

func stepOf(p Plan, id StepID) Step {
	for _, s := range p.Steps {
		if s.ID == id {
			return s
		}
	}
	return Step{}
}

// TestBuildSteps has a row for every state of every step. Load-bearing: the
// broken-settings row (Cannot, never overwritten), the Unknown login rows
// (could not tell reads as to do, not done), and the ~/bin rows (the service
// needs ~/bin/afk; a missing PATH entry holds the final check back).
func TestBuildSteps(t *testing.T) {
	cases := []struct {
		name      string
		mod       func(f *Facts)
		id        StepID
		state     State
		reason    string // substring of Reason, if non-empty
		fix       string // substring of Fix, if non-empty
		wantNoFix bool
	}{
		{"safety vm done", func(f *Facts) {}, StepSafety, Done, "", "", true},
		{"safety dedicated done", func(f *Facts) { f.Virt = VirtNone }, StepSafety, Done, "", "", true},
		{"safety desktop todo", func(f *Facts) { f.Desktop = true }, StepSafety, ToDo, "I accept the risk", "", true},
		{"safety unknown todo", func(f *Facts) { f.Virt = VirtUnknown }, StepSafety, ToDo, "Multipass", "", true},
		{"packages done", func(f *Facts) {}, StepPackages, Done, "", "", true},
		{"packages todo apt", func(f *Facts) { *f = without(*f, "jq") }, StepPackages, ToDo, "jq", "", true},
		{"packages todo brew", func(f *Facts) { f.OS = "darwin"; f.PackageManager = "brew"; *f = without(*f, "ruby") }, StepPackages, ToDo, "ruby", "", true},
		{"packages cannot mac no brew", func(f *Facts) { f.OS = "darwin"; f.PackageManager = ""; *f = without(*f, "gh") }, StepPackages, Cannot, "", "Install Homebrew from brew.sh, then run afk install again", false},
		{"packages cannot linux no manager", func(f *Facts) { f.PackageManager = ""; *f = without(*f, "gh"); *f = without(*f, "jq") }, StepPackages, Cannot, "", "Install gh, jq with your system's package manager, then run afk install again.", false},
		{"packages cannot linux with brew", func(f *Facts) { f.PackageManager = "brew"; *f = without(*f, "gh") }, StepPackages, Cannot, "", "package manager", false},
		{"agent cli done", func(f *Facts) {}, StepAgentCLI, Done, "", "", true},
		{"agent cli todo", func(f *Facts) { f.AgentCLI.Found = false }, StepAgentCLI, ToDo, "not installed", "", true},
		{"login done", func(f *Facts) {}, StepAgentLogin, Done, "", "", true},
		{"login todo no cli", func(f *Facts) { f.AgentCLI.Found = false; f.AgentLogin = Yes }, StepAgentLogin, ToDo, "after the agent CLI is installed", "", true},
		{"login todo no", func(f *Facts) { f.AgentLogin = No }, StepAgentLogin, ToDo, "not signed in", "", true},
		{"login todo unknown", func(f *Facts) { f.AgentLogin = Unknown }, StepAgentLogin, ToDo, "could not tell", "", true},
		{"unattended set done", func(f *Facts) {}, StepUnattended, Done, "", "", true},
		{"unattended not set todo", func(f *Facts) { f.Unattended = provider.UnattendedNotSet }, StepUnattended, ToDo, "afk will turn off", "", true},
		{"unattended broken cannot", func(f *Facts) {
			f.Unattended = provider.UnattendedBroken
			f.UnattendedProblem = "Fix or remove ~/x.json."
		}, StepUnattended, Cannot, "will not overwrite", "Fix or remove ~/x.json.", false},
		{"unattended unsupported cannot", func(f *Facts) { f.Unattended = provider.UnattendedUnsupported }, StepUnattended, Cannot, "no unattended mode afk knows", "unattended", false},
		{"plugin done", func(f *Facts) {}, StepPlugin, Done, "", "", true},
		{"plugin todo", func(f *Facts) { f.AgentPlugin = No }, StepPlugin, ToDo, "not installed yet", "", true},
		{"plugin todo unknown", func(f *Facts) { f.AgentPlugin = Unknown }, StepPlugin, ToDo, "not installed yet", "", true},
		{"plugin todo no cli", func(f *Facts) { f.AgentCLI.Found = false; f.AgentPlugin = No }, StepPlugin, ToDo, "after the agent CLI", "", true},
		{"github done", func(f *Facts) {}, StepGitHubLogin, Done, "", "", true},
		{"github todo no gh", func(f *Facts) { *f = without(*f, "gh"); f.GitHubLogin = No }, StepGitHubLogin, ToDo, "after the packages", "", true},
		{"github todo not signed in", func(f *Facts) { f.GitHubLogin = No }, StepGitHubLogin, ToDo, "not signed in", "", true},
		{"github todo unknown", func(f *Facts) { f.GitHubLogin = Unknown }, StepGitHubLogin, ToDo, "could not tell", "", true},
		{"github todo git not using it", func(f *Facts) { f.GitUsesGitHub = No }, StepGitHubLogin, ToDo, "git is not set up", "", true},
		{"identity done", func(f *Facts) {}, StepGitIdentity, Done, "", "", true},
		{"identity todo name", func(f *Facts) { f.GitName = "" }, StepGitIdentity, ToDo, "your name;", "", true},
		{"identity todo email", func(f *Facts) { f.GitEmail = "" }, StepGitIdentity, ToDo, "your email;", "", true},
		{"identity todo both", func(f *Facts) { f.GitName, f.GitEmail = "", "" }, StepGitIdentity, ToDo, "name and email", "", true},
		{"location done", func(f *Facts) {}, StepProjectLocation, Done, "/home/u/code", "", true},
		{"location todo", func(f *Facts) { f.ProjectLocationSet = false }, StepProjectLocation, ToDo, "~/code", "", true},
		{"location cannot", func(f *Facts) { f.ProjectLocationProblem = "That folder is a file." }, StepProjectLocation, Cannot, "", "That folder is a file.", false},
		{"web done", func(f *Facts) {}, StepWebPage, Done, "", "", true},
		{"web todo no file", func(f *Facts) { f.WebPassword = No }, StepWebPage, ToDo, "no password yet", "", true},
		{"web todo unreadable", func(f *Facts) { f.WebPassword = Unknown }, StepWebPage, ToDo, "cannot be read; you will set a new one", "", true},
		{"service done", func(f *Facts) {}, StepService, Done, "", "", true},
		{"service cannot no afk in bin", func(f *Facts) { f.BinHasAfk = false }, StepService, Cannot, "", "Put afk in ~/bin first", false},
		{"service todo not installed", func(f *Facts) { f.Service.Installed = No }, StepService, ToDo, "installed", "", true},
		{"service todo not enabled", func(f *Facts) { f.Service.Enabled = Unknown }, StepService, ToDo, "enabled", "", true},
		{"service todo not running", func(f *Facts) { f.Service.Running = No }, StepService, ToDo, "running", "", true},
		{"service todo no linger on linux", func(f *Facts) { f.Service.Linger = No }, StepService, ToDo, "log out", "", true},
		{"service done on mac without linger", func(f *Facts) { f.OS = "darwin"; f.Service.Linger = "" }, StepService, Done, "", "", true},
		{"check done", func(f *Facts) {}, StepCheck, Done, "", "", true},
		{"check todo steps open", func(f *Facts) { f.WebPassword = No }, StepCheck, ToDo, "once the steps", "", true},
		{"check todo bin not on path", func(f *Facts) { f.BinOnPath = false }, StepCheck, ToDo, "PATH", "", true},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			f := full()
			tc.mod(&f)
			s := stepOf(Build(f), tc.id)
			if s.State != tc.state {
				t.Fatalf("state = %q, want %q (reason %q)", s.State, tc.state, s.Reason)
			}
			if s.Reason == "" {
				t.Errorf("empty reason")
			}
			if tc.reason != "" && !strings.Contains(s.Reason, tc.reason) {
				t.Errorf("reason %q lacks %q", s.Reason, tc.reason)
			}
			if tc.fix != "" && !strings.Contains(s.Fix, tc.fix) {
				t.Errorf("fix %q lacks %q", s.Fix, tc.fix)
			}
			if tc.wantNoFix && s.Fix != "" {
				t.Errorf("fix = %q, want empty for a non-cannot step", s.Fix)
			}
			if s.State == Cannot && s.Fix == "" {
				t.Errorf("cannot step has no fix")
			}
		})
	}
}

// TestBuildRisk: the cloud VM, Multipass VM, desktop, bare metal, unknown and
// Mac rows are the master plan's list. Load-bearing: the desktop row beats
// virtualisation (a VM on a daily-use desktop is personal) and unknown stays
// unknown rather than guessing dedicated.
func TestBuildRisk(t *testing.T) {
	cases := []struct {
		name string
		mod  func(f *Facts)
		want Risk
		safe State
	}{
		{"cloud vm kvm no desktop", func(f *Facts) { f.Virt = VirtVM }, RiskVM, Done},
		{"multipass vm qemu no desktop", func(f *Facts) { f.Virt = VirtVM; f.Arch = "arm64" }, RiskVM, Done},
		{"container", func(f *Facts) { f.Virt = VirtContainer }, RiskVM, Done},
		{"linux desktop", func(f *Facts) { f.Virt = VirtNone; f.Desktop = true }, RiskPersonal, ToDo},
		{"vm with desktop", func(f *Facts) { f.Virt = VirtVM; f.Desktop = true }, RiskPersonal, ToDo},
		{"bare metal headless", func(f *Facts) { f.Virt = VirtNone }, RiskDedicated, Done},
		{"virt unknown", func(f *Facts) { f.Virt = VirtUnknown }, RiskUnknown, ToDo},
		{"mac", func(f *Facts) { f.OS = "darwin"; f.Virt = VirtNone; f.PackageManager = "brew" }, RiskPersonal, ToDo},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			f := full()
			tc.mod(&f)
			p := Build(f)
			if p.Risk != tc.want {
				t.Errorf("risk = %q, want %q", p.Risk, tc.want)
			}
			if got := stepOf(p, StepSafety).State; got != tc.safe {
				t.Errorf("safety = %q, want %q", got, tc.safe)
			}
		})
	}
}

// TestBuildCannotAll: root and an unsupported system turn every step into
// Cannot with the one blanket message, and the risk is still computed.
func TestBuildCannotAll(t *testing.T) {
	cases := []struct {
		name string
		mod  func(f *Facts)
		fix  string
		risk Risk
	}{
		{"root", func(f *Facts) { f.Root = true }, "Run afk install as your normal user, not root.", RiskVM},
		{"root on a desktop", func(f *Facts) { f.Root = true; f.Desktop = true }, "Run afk install as your normal user, not root.", RiskPersonal},
		{"unsupported os", func(f *Facts) { f.OS = "freebsd"; f.Virt = VirtNone }, "afk install works on Linux and macOS only.", RiskDedicated},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			f := full()
			tc.mod(&f)
			p := Build(f)
			if p.Risk != tc.risk {
				t.Errorf("risk = %q, want %q", p.Risk, tc.risk)
			}
			if len(p.Steps) != 12 {
				t.Fatalf("steps = %d, want 12", len(p.Steps))
			}
			for _, s := range p.Steps {
				if s.State != Cannot || !strings.HasPrefix(s.Fix, tc.fix) {
					t.Errorf("%s: state %q fix %q", s.ID, s.State, s.Fix)
				}
			}
		})
	}
}

// TestBuildFullyInstalled: a fully installed Linux VM has every step Done. On
// a Mac the safety step stays to do (it is a personal computer, so the person
// types the acceptance each run) and the final check waits on it.
func TestBuildFullyInstalled(t *testing.T) {
	mac := full()
	mac.OS, mac.Virt, mac.PackageManager = "darwin", VirtNone, "brew"
	mac.Service.Linger = ""
	for name, f := range map[string]Facts{"linux": full(), "mac": mac} {
		t.Run(name, func(t *testing.T) {
			for _, s := range Build(f).Steps {
				want := Done
				if name == "mac" && (s.ID == StepSafety || s.ID == StepCheck) {
					want = ToDo
				}
				if s.State != want {
					t.Errorf("%s: %q, want %q (%s)", s.ID, s.State, want, s.Reason)
				}
			}
		})
	}
}

// TestBuildStepOrder: the twelve IDs, in install order, for every kind of plan.
func TestBuildStepOrder(t *testing.T) {
	want := []StepID{StepSafety, StepPackages, StepAgentCLI, StepAgentLogin, StepUnattended, StepPlugin,
		StepGitHubLogin, StepGitIdentity, StepProjectLocation, StepWebPage, StepService, StepCheck}
	root := full()
	root.Root = true
	for name, f := range map[string]Facts{"full": full(), "root": root, "empty": {OS: "linux"}} {
		t.Run(name, func(t *testing.T) {
			p := Build(f)
			if len(p.Steps) != len(want) {
				t.Fatalf("steps = %d", len(p.Steps))
			}
			for i, s := range p.Steps {
				if s.ID != want[i] {
					t.Errorf("step %d = %q, want %q", i, s.ID, want[i])
				}
				if s.Title == "" {
					t.Errorf("step %d has no title", i)
				}
			}
		})
	}
}

// TestBuildNamesNoProvider: the package says "the agent CLI", never a vendor.
// The names are split so this file does not contain them either.
func TestBuildNamesNoProvider(t *testing.T) {
	banned := []string{"clau" + "de", "co" + "dex", "open" + "code", "anthro" + "pic", "open" + "ai"}
	var all []Facts
	all = append(all, full(), Facts{}, Facts{OS: "darwin"}, Facts{OS: "linux", Root: true})
	for _, u := range []provider.UnattendedSetting{provider.UnattendedNotSet, provider.UnattendedBroken, provider.UnattendedUnsupported} {
		f := full()
		f.Unattended = u
		f.AgentCLI.Found = false
		f.AgentLogin, f.AgentPlugin, f.GitHubLogin = Unknown, No, No
		all = append(all, f)
	}
	for i, f := range all {
		for _, s := range Build(f).Steps {
			text := strings.ToLower(s.Title + " " + s.Reason + " " + s.Fix)
			for _, b := range banned {
				if strings.Contains(text, b) {
					t.Errorf("facts %d step %s mentions %q: %s", i, s.ID, b, text)
				}
			}
		}
	}
}
