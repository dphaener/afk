package provider

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestUnattended: "argv is copied" is load-bearing, since a daemon that
// mutated the shared launch argv would make later attended launches
// unattended. "launch" and "resume" pin the flag directly after the program
// name. "empty argv" must error, not panic on argv[0]. The unsupported rows
// guard against silently launching a provider with no known safe mode.
func TestUnattended(t *testing.T) {
	launch := LaunchArgvFor(t)
	cases := []struct {
		name string
		p    Provider
		argv []string
		want []string
		err  error
	}{
		{"launch argv", claude{}, launch, append([]string{launch[0], "--permission-mode", "bypassPermissions"}, launch[1:]...), nil},
		{"resume argv", claude{}, []string{"claude", "--resume", "abc"}, []string{"claude", "--permission-mode", "bypassPermissions", "--resume", "abc"}, nil},
		{"codex unsupported", codex{}, []string{"codex"}, nil, ErrUnattendedUnsupported},
		{"opencode unsupported", opencode{}, []string{"opencode"}, nil, ErrUnattendedUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.p.Unattended(tc.argv)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("argv = %v", got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("argv is copied not mutated", func(t *testing.T) {
		in := make([]string, 3, 10)
		copy(in, []string{"claude", "a", "b"})
		orig := append([]string(nil), in...)
		out, err := claude{}.Unattended(in)
		if err != nil {
			t.Fatal(err)
		}
		out[1] = "changed"
		if !reflect.DeepEqual(in, orig) || in[:cap(in)][3] != "" {
			t.Fatalf("input mutated: %v", in[:cap(in)])
		}
	})

	t.Run("empty argv", func(t *testing.T) {
		got, err := claude{}.Unattended(nil)
		if err == nil || len(got) != 0 {
			t.Fatalf("got %v, %v; want unchanged and an error", got, err)
		}
	})
}

// LaunchArgvFor returns a real launch argv so the test follows it if it changes.
func LaunchArgvFor(t *testing.T) []string {
	t.Helper()
	return claude{}.LaunchArgv("s", false, true)
}

// TestUnattendedPreflight: "root" is load-bearing because the CLI refuses the
// mode for root and a pass there would fail only at launch. "key false",
// "key absent", "bad JSON" and "missing file" each leave the confirmation
// prompt in place, which would hang an unattended session. Every failing row
// must give plain-language steps.
func TestUnattendedPreflight(t *testing.T) {
	read := func(data string, err error) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if path != "/h/.claude/settings.json" {
				t.Errorf("read %q, want /h/.claude/settings.json", path)
			}
			return []byte(data), err
		}
	}
	user := func() int { return 1000 }
	cases := []struct {
		name string
		d    UnattendedDeps
		ok   bool
	}{
		{"ok", UnattendedDeps{"/h", user, read(`{"skipDangerousModePermissionPrompt": true}`, nil)}, true},
		{"root", UnattendedDeps{"/h", func() int { return 0 }, read(`{"skipDangerousModePermissionPrompt": true}`, nil)}, false},
		{"missing file", UnattendedDeps{"/h", user, read("", errors.New("no such file"))}, false},
		{"bad JSON", UnattendedDeps{"/h", user, read(`{nope`, nil)}, false},
		{"key false", UnattendedDeps{"/h", user, read(`{"skipDangerousModePermissionPrompt": false}`, nil)}, false},
		{"key absent", UnattendedDeps{"/h", user, read(`{}`, nil)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, steps := claude{}.UnattendedPreflight(tc.d)
			t.Logf("ok=%v steps=%q", ok, steps)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok && !strings.Contains(steps, "Do not run the afk daemon as root") && !strings.Contains(steps, "root") {
				t.Fatalf("steps not plain-language guidance: %q", steps)
			}
			if ok && steps != "" {
				t.Fatalf("steps = %q, want empty", steps)
			}
		})
	}

	for _, p := range []Provider{codex{}, opencode{}} {
		t.Run("unsupported "+p.Name(), func(t *testing.T) {
			ok, steps := p.UnattendedPreflight(UnattendedDeps{})
			if ok || steps == "" {
				t.Fatalf("got %v, %q; want not ok with a sentence", ok, steps)
			}
		})
	}
}
