package install

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/webpass"
)

// Probe has no write-shaped dependency: Deps offers no WriteFile, Rename,
// MkdirAll or Remove, so "Probe changes nothing" holds by construction and
// needs no test of its own.

type exitErr struct{ code int }

func (e exitErr) Error() string { return "exit status" }
func (e exitErr) ExitCode() int { return e.code }

type cmdResult struct {
	out string
	err error
	// block makes the fake wait for the context, like a hung command.
	block bool
}

// machine is a fake computer: files, programs on PATH and command answers.
type machine struct {
	mu       sync.Mutex
	files    map[string]string
	paths    map[string]string
	cmds     map[string]cmdResult
	noDeadln []string
	ran      []string
}

func (m *machine) deps() Deps {
	return Deps{
		GOOS: "linux", GOARCH: "amd64", Timeout: 20 * time.Millisecond * 10,
		Euid:    func() int { return 1000 },
		Getenv:  func(k string) string { return map[string]string{"USER": "u", "PATH": "/usr/bin:/home/u/bin"}[k] },
		HomeDir: func() (string, error) { return "/home/u", nil },
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := m.files[p]; ok {
				return []byte(s), nil
			}
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		},
		Stat: func(p string) (fs.FileInfo, error) {
			if _, ok := m.files[p]; ok {
				return nil, nil
			}
			return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
		},
		LookPath: func(n string) (string, error) {
			if p, ok := m.paths[n]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Rex:          func(context.Context) (bool, bool) { _, ok := m.paths["rex"]; return ok, ok },
		Executable:   func() (string, error) { return "/home/u/bin/afk", nil },
		EvalSymlinks: func(p string) (string, error) { return p, nil },
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			key := strings.Join(append([]string{name}, args...), " ")
			m.mu.Lock()
			m.ran = append(m.ran, key)
			if _, ok := ctx.Deadline(); !ok {
				m.noDeadln = append(m.noDeadln, key)
			}
			r, ok := m.cmds[key]
			m.mu.Unlock()
			if !ok {
				return nil, errors.New("exec: not found: " + name)
			}
			if r.block {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []byte(r.out), r.err
		},
	}
}

func fullMachine(t *testing.T) *machine {
	t.Helper()
	cli, err := provider.CLI("")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := webpass.New("a long enough password", rand.Reader, 1000)
	if err != nil {
		t.Fatal(err)
	}
	rj, _ := json.Marshal(rec)
	paths := map[string]string{"apt-get": "/usr/bin/apt-get", cli: "/usr/bin/" + cli}
	for _, n := range []string{"git", "gh", "jq", "ruby"} {
		paths[n] = "/usr/bin/" + n
	}
	return &machine{
		files: map[string]string{
			"/etc/os-release":                            "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n",
			"/home/u/." + cli + "/settings.json":         `{"skipDangerousModePermissionPrompt": true}`,
			"/home/u/.config/afk/profile.toml":           "[[entry]]\ntopic = \"project location\"\nvalue = \"/home/u/code\"\n",
			"/home/u/.local/state/afk/web-password.json": string(rj),
			"/home/u/bin/afk":                            "",
			"/home/u/.config/afk/config.toml":            "[runtime]\nbackend = \"rex\"\n",
			"/home/u/.config/systemd/user/afkd.service":  "",
		},
		paths: paths,
		cmds: map[string]cmdResult{
			"systemd-detect-virt --vm": {out: "kvm\n"},
			cli + " auth status":       {out: `{"loggedIn":true}`},
			cli + " plugin list":       {out: "afk@marketplace\n"},
			"gh auth status":           {},
			"git config --global --get-all credential.https://github.com.helper": {out: "!/usr/bin/gh auth git-credential\n"},
			"git config --global user.name":                                      {out: "Ada\n"},
			"git config --global user.email":                                     {out: "ada@example.com\n"},
			"systemctl --user is-enabled afkd":                                   {out: "enabled\n"},
			"systemctl --user is-active afkd":                                    {out: "active\n"},
			"loginctl show-user u --property=Linger":                             {out: "Linger=yes\n"},
		},
	}
}

// TestProbeFullMachine ties Probe to Build: a fully installed Linux VM must
// read as thirteen Done steps, so a Facts field the probe forgets to fill shows
// up here as a step that is not Done.
func TestProbeFullMachine(t *testing.T) {
	m := fullMachine(t)
	f := Probe(m.deps())
	plan := Build(f)
	if plan.Risk != RiskVM {
		t.Errorf("risk = %q, want vm", plan.Risk)
	}
	if len(plan.Steps) != 13 {
		t.Fatalf("got %d steps", len(plan.Steps))
	}
	for _, s := range plan.Steps {
		if s.State != Done {
			t.Errorf("step %s = %s (%s %s)", s.ID, s.State, s.Reason, s.Fix)
		}
	}
	if f.Distro != "ubuntu" || f.DistroVersion != "24.04" {
		t.Errorf("os-release read as %q %q", f.Distro, f.DistroVersion)
	}
}

// TestProbeRows derives each row from the full machine. Load-bearing rows:
// "timeout" (Unknown, never No, and prompt), "settings unreadable" (Broken,
// not NotSet) and "detect-virt none with exit 1" (the command prints its
// answer while failing).
func TestProbeRows(t *testing.T) {
	cli, _ := provider.CLI("")
	settings := "/home/u/." + cli + "/settings.json"
	dmg := "/home/u/.local/state/afk/web-password.json"
	cases := []struct {
		name  string
		tweak func(*machine, *Deps)
		check func(*testing.T, Facts)
	}{
		{"missing tool", func(m *machine, _ *Deps) { delete(m.paths, "jq") }, func(t *testing.T, f Facts) {
			if f.Tools[2].Name != "jq" || f.Tools[2].Found {
				t.Errorf("tools = %+v", f.Tools)
			}
		}},
		{"missing agent cli", func(m *machine, _ *Deps) { delete(m.paths, cli) }, func(t *testing.T, f Facts) {
			if f.AgentCLI.Found || f.AgentLogin != Unknown || f.AgentPlugin != Unknown {
				t.Errorf("agent = %+v %s %s", f.AgentCLI, f.AgentLogin, f.AgentPlugin)
			}
		}},
		{"timeout", func(m *machine, d *Deps) {
			d.Timeout = 5 * time.Millisecond
			m.cmds["gh auth status"] = cmdResult{block: true}
			m.cmds[cli+" auth status"] = cmdResult{block: true}
			m.cmds[cli+" plugin list"] = cmdResult{block: true}
			m.cmds["systemd-detect-virt --vm"] = cmdResult{block: true}
		}, func(t *testing.T, f Facts) {
			if f.GitHubLogin != Unknown || f.AgentLogin != Unknown || f.AgentPlugin != Unknown || f.Virt != VirtUnknown {
				t.Errorf("got %s %s %s %s", f.GitHubLogin, f.AgentLogin, f.AgentPlugin, f.Virt)
			}
		}},
		{"settings unreadable", func(m *machine, d *Deps) {
			rf := d.ReadFile
			d.ReadFile = func(p string) ([]byte, error) {
				if p == settings {
					return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
				}
				return rf(p)
			}
		}, func(t *testing.T, f Facts) {
			if f.Unattended != provider.UnattendedBroken || f.UnattendedProblem == "" {
				t.Errorf("unattended = %s %q", f.Unattended, f.UnattendedProblem)
			}
		}},
		{"settings missing", func(m *machine, _ *Deps) { delete(m.files, settings) }, func(t *testing.T, f Facts) {
			if f.Unattended != provider.UnattendedNotSet {
				t.Errorf("unattended = %s", f.Unattended)
			}
		}},
		{"detect-virt none with exit 1", func(m *machine, _ *Deps) {
			m.cmds["systemd-detect-virt --vm"] = cmdResult{out: "none\n", err: exitErr{1}}
			m.cmds["systemd-detect-virt --container"] = cmdResult{out: "none\n", err: exitErr{1}}
		}, func(t *testing.T, f Facts) {
			if f.Virt != VirtNone {
				t.Errorf("virt = %s", f.Virt)
			}
		}},
		{"container", func(m *machine, _ *Deps) {
			m.cmds["systemd-detect-virt --vm"] = cmdResult{out: "none\n", err: exitErr{1}}
			m.cmds["systemd-detect-virt --container"] = cmdResult{out: "lxc\n"}
		}, func(t *testing.T, f Facts) {
			if f.Virt != VirtContainer {
				t.Errorf("virt = %s", f.Virt)
			}
		}},
		{"detect-virt missing", func(m *machine, _ *Deps) { delete(m.cmds, "systemd-detect-virt --vm") }, func(t *testing.T, f Facts) {
			if f.Virt != VirtUnknown {
				t.Errorf("virt = %s", f.Virt)
			}
		}},
		{"os-release quoted", func(m *machine, _ *Deps) {
			m.files["/etc/os-release"] = "ID=\"debian\"\nVERSION_ID='12'\n"
		}, func(t *testing.T, f Facts) {
			if f.Distro != "debian" || f.DistroVersion != "12" {
				t.Errorf("got %q %q", f.Distro, f.DistroVersion)
			}
		}},
		{"os-release unreadable", func(m *machine, _ *Deps) { delete(m.files, "/etc/os-release") }, func(t *testing.T, f Facts) {
			if f.Distro != "" || f.DistroVersion != "" {
				t.Errorf("got %q %q", f.Distro, f.DistroVersion)
			}
		}},
		{"root", func(_ *machine, d *Deps) { d.Euid = func() int { return 0 } }, func(t *testing.T, f Facts) {
			if !f.Root {
				t.Error("root not read")
			}
		}},
		{"desktop by env", func(_ *machine, d *Deps) {
			d.Getenv = func(k string) string { return map[string]string{"XDG_SESSION_TYPE": "wayland"}[k] }
		}, func(t *testing.T, f Facts) {
			if !f.Desktop || f.BinOnPath {
				t.Errorf("desktop=%v binOnPath=%v", f.Desktop, f.BinOnPath)
			}
		}},
		{"desktop by display manager", func(m *machine, _ *Deps) {
			m.files["/etc/systemd/system/display-manager.service"] = ""
		}, func(t *testing.T, f Facts) {
			if !f.Desktop {
				t.Error("desktop not read")
			}
		}},
		{"gh logged out", func(m *machine, _ *Deps) { m.cmds["gh auth status"] = cmdResult{err: exitErr{1}} }, func(t *testing.T, f Facts) {
			if f.GitHubLogin != No {
				t.Errorf("got %s", f.GitHubLogin)
			}
		}},
		{"gh missing", func(m *machine, _ *Deps) { delete(m.cmds, "gh auth status") }, func(t *testing.T, f Facts) {
			if f.GitHubLogin != Unknown {
				t.Errorf("got %s", f.GitHubLogin)
			}
		}},
		{"git does not use gh credentials", func(m *machine, _ *Deps) {
			m.cmds["git config --global --get-all credential.https://github.com.helper"] = cmdResult{err: exitErr{1}}
		}, func(t *testing.T, f Facts) {
			if f.GitUsesGitHub != No {
				t.Errorf("got %s", f.GitUsesGitHub)
			}
		}},
		{"no git identity", func(m *machine, _ *Deps) {
			m.cmds["git config --global user.name"] = cmdResult{err: exitErr{1}}
			delete(m.cmds, "git config --global user.email")
		}, func(t *testing.T, f Facts) {
			if f.GitName != "" || f.GitEmail != "" {
				t.Errorf("got %q %q", f.GitName, f.GitEmail)
			}
		}},
		{"agent logged out", func(m *machine, _ *Deps) {
			m.cmds[cli+" auth status"] = cmdResult{out: `{"loggedIn":false}`, err: exitErr{1}}
		}, func(t *testing.T, f Facts) {
			if f.AgentLogin != No {
				t.Errorf("got %s", f.AgentLogin)
			}
		}},
		{"plugin missing", func(m *machine, _ *Deps) { m.cmds[cli+" plugin list"] = cmdResult{out: "other\n"} }, func(t *testing.T, f Facts) {
			if f.AgentPlugin != No {
				t.Errorf("got %s", f.AgentPlugin)
			}
		}},
		{"no password file", func(m *machine, _ *Deps) { delete(m.files, dmg) }, func(t *testing.T, f Facts) {
			if f.WebPassword != No {
				t.Errorf("got %s", f.WebPassword)
			}
		}},
		{"damaged password file", func(m *machine, _ *Deps) { m.files[dmg] = "{not json" }, func(t *testing.T, f Facts) {
			if f.WebPassword != Unknown {
				t.Errorf("got %s", f.WebPassword)
			}
		}},
		{"web bind default", func(_ *machine, _ *Deps) {}, func(t *testing.T, f Facts) {
			if f.WebBind == "" {
				t.Error("web bind empty")
			}
		}},
		{"no profile", func(m *machine, _ *Deps) { delete(m.files, "/home/u/.config/afk/profile.toml") }, func(t *testing.T, f Facts) {
			if f.ProjectLocationSet || f.ProjectLocationProblem != "" {
				t.Errorf("got %+v", f)
			}
		}},
		{"relative project location", func(m *machine, _ *Deps) {
			m.files["/home/u/.config/afk/profile.toml"] = "[[entry]]\ntopic = \"project location\"\nvalue = \"code\"\n"
		}, func(t *testing.T, f Facts) {
			if f.ProjectLocationProblem == "" {
				t.Error("no problem reported")
			}
		}},
		{"bin not on path, not this binary", func(m *machine, d *Deps) {
			d.Getenv = func(k string) string { return map[string]string{"PATH": "/usr/bin"}[k] }
			d.EvalSymlinks = func(p string) (string, error) { return p + ".x", nil }
			d.Executable = func() (string, error) { return "/tmp/afk", nil }
		}, func(t *testing.T, f Facts) {
			if f.BinOnPath || f.BinIsThis || !f.BinHasAfk {
				t.Errorf("onPath=%v isThis=%v has=%v", f.BinOnPath, f.BinIsThis, f.BinHasAfk)
			}
		}},
		{"bin path with trailing slash", func(_ *machine, d *Deps) {
			d.Getenv = func(k string) string { return map[string]string{"PATH": "/usr/bin:/home/u/bin/"}[k] }
		}, func(t *testing.T, f Facts) {
			if !f.BinOnPath {
				t.Error("cleaned path not matched")
			}
		}},
		{"service stopped", func(m *machine, _ *Deps) {
			m.cmds["systemctl --user is-active afkd"] = cmdResult{out: "inactive\n", err: exitErr{3}}
			m.cmds["loginctl show-user u --property=Linger"] = cmdResult{out: "Linger=no\n"}
		}, func(t *testing.T, f Facts) {
			if f.Service.Running != No || f.Service.Linger != No || f.Service.Enabled != Yes {
				t.Errorf("service = %+v", f.Service)
			}
		}},
		{"systemctl without a bus", func(m *machine, _ *Deps) {
			m.cmds["systemctl --user is-active afkd"] = cmdResult{err: exitErr{1}}
		}, func(t *testing.T, f Facts) {
			if f.Service.Running != Unknown {
				t.Errorf("got %s", f.Service.Running)
			}
		}},
		{"tailscale", func(m *machine, _ *Deps) { m.paths["tailscale"] = "/usr/bin/tailscale" }, func(t *testing.T, f Facts) {
			if !f.Tailscale {
				t.Error("tailscale not found")
			}
		}},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			m := fullMachine(t)
			d := m.deps()
			c.tweak(m, &d)
			start := time.Now()
			f := Probe(d)
			if el := time.Since(start); el > time.Second {
				t.Errorf("probe took %s", el)
			}
			c.check(t, f)
		})
	}
}

// TestProbeMac: macOS is never virtual-unknown, always a desktop, and the
// service comes from launchctl. The not-loaded row is the "no" answer.
func TestProbeMac(t *testing.T) {
	label := LaunchAgentLabel
	mac := func(m *machine) Deps {
		d := m.deps()
		d.GOOS, d.GOARCH = "darwin", "arm64"
		delete(m.paths, "apt-get")
		m.paths["brew"] = "/opt/homebrew/bin/brew"
		m.files["/home/u/Library/LaunchAgents/"+label+".plist"] = ""
		return d
	}
	run := "launchctl print gui/1000/" + label
	t.Run("running", func(t *testing.T) {
		m := fullMachine(t)
		m.cmds[run] = cmdResult{out: "state = running\n"}
		f := Probe(mac(m))
		if f.Virt != VirtNone || !f.Desktop || f.PackageManager != "brew" || f.Distro != "" {
			t.Errorf("facts = %+v", f)
		}
		if f.Service != (ServiceFacts{Installed: Yes, Enabled: Yes, Running: Yes, Linger: Unknown}) {
			t.Errorf("service = %+v", f.Service)
		}
		if got := Build(f).Risk; got != RiskPersonal {
			t.Errorf("risk = %s", got)
		}
	})
	t.Run("loaded but waiting", func(t *testing.T) {
		m := fullMachine(t)
		m.cmds[run] = cmdResult{out: "state = waiting\n"}
		if s := Probe(mac(m)).Service; s.Enabled != Yes || s.Running != No {
			t.Errorf("service = %+v", s)
		}
	})
	t.Run("not loaded", func(t *testing.T) {
		m := fullMachine(t)
		m.cmds[run] = cmdResult{err: exitErr{113}}
		if s := Probe(mac(m)).Service; s.Enabled != No || s.Running != No {
			t.Errorf("service = %+v", s)
		}
	})
}

// TestProbeEveryCommandHasDeadline: the fake records any command whose
// context has no deadline.
func TestProbeEveryCommandHasDeadline(t *testing.T) {
	m := fullMachine(t)
	Probe(m.deps())
	if len(m.ran) < 10 {
		t.Fatalf("only %d commands ran: %v", len(m.ran), m.ran)
	}
	if len(m.noDeadln) != 0 {
		t.Errorf("commands without a deadline: %v", m.noDeadln)
	}
}

// TestNoProviderNamesInPackage keeps every file of this package, tests
// included, free of agent vendor names; provider.CLI supplies the command.
func TestNoProviderNamesInPackage(t *testing.T) {
	banned := []string{"clau" + "de", "co" + "dex", "open" + "code", "anthro" + "pic", "open" + "ai"}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 4 {
		t.Fatalf("glob: %v %v", files, err)
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		low := strings.ToLower(string(data))
		for _, b := range banned {
			if strings.Contains(low, b) {
				t.Errorf("%s names %q", name, b)
			}
		}
	}
}
