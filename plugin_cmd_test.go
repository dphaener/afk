package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/pluginstall"
)

// TestResolveInstallTargets pins who decides what gets installed.
// Load-bearing rows: "no flag, not interactive" must refuse rather than
// install into everything detected (the original behavior this replaced);
// "explicit target" must never prompt, since the flag is the consent and
// scripts depend on it; and "user picks a subset" must install exactly the
// subset, not everything detected.
func TestResolveInstallTargets(t *testing.T) {
	all := []pluginstall.Target{pluginstall.Claude, pluginstall.Codex, pluginstall.OpenCode}
	cases := []struct {
		name        string
		flag        string
		detected    []pluginstall.Target
		interactive bool
		pickRet     []pluginstall.Target
		pickErr     error
		want        []pluginstall.Target
		wantErr     error
		wantErrSub  string
		wantPrompt  bool
	}{
		{name: "explicit target", flag: "codex", detected: all, interactive: true, want: []pluginstall.Target{pluginstall.Codex}},
		{name: "explicit target non-interactive", flag: "all", interactive: false, want: all},
		{name: "explicit bad target", flag: "cursor", interactive: true, wantErrSub: "unknown target"},
		{name: "no flag not interactive", detected: all, interactive: false, wantErr: errNeedTarget},
		{name: "no flag nothing detected", interactive: true, wantErrSub: "no agent CLI found"},
		{name: "user picks a subset", detected: all, interactive: true, pickRet: []pluginstall.Target{pluginstall.OpenCode}, want: []pluginstall.Target{pluginstall.OpenCode}, wantPrompt: true},
		{name: "user picks nothing", detected: all, interactive: true, pickRet: nil, want: nil, wantPrompt: true},
		{name: "user aborts", detected: all, interactive: true, pickErr: errors.New("user aborted"), wantErr: errPickAborted, wantPrompt: true},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			prompted := false
			pick := func(d []pluginstall.Target) ([]pluginstall.Target, error) {
				prompted = true
				if !reflect.DeepEqual(d, tt.detected) {
					t.Errorf("prompt offered %v, want %v", d, tt.detected)
				}
				return tt.pickRet, tt.pickErr
			}
			got, err := resolveInstallTargets(tt.flag, tt.detected, tt.interactive, pick)
			if prompted != tt.wantPrompt {
				t.Errorf("prompted = %v, want %v", prompted, tt.wantPrompt)
			}
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			case tt.wantErrSub != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErrSub)
				}
				return
			case err != nil:
				t.Fatalf("unexpected err: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunPluginInstallConsent drives the whole command with deps whose Run
// records every external command. Without --target and without a terminal
// nothing may run; an empty pick exits 0 having run nothing; an abort exits 1.
func TestRunPluginInstallConsent(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		interactive bool
		pickRet     []pluginstall.Target
		pickErr     error
		wantCode    int
		wantOut     string
	}{
		{name: "script without target", interactive: false, wantCode: 2, wantOut: "pass --target"},
		{name: "empty pick", interactive: true, wantCode: 0, wantOut: "Nothing selected"},
		{name: "abort", interactive: true, pickErr: errors.New("ctrl-c"), wantCode: 1, wantOut: "aborted, no changes made"},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			var ran []string
			var out bytes.Buffer
			home := t.TempDir()
			d := pluginstall.Deps{
				Getenv:   func(string) string { return "" },
				HomeDir:  func() (string, error) { return home, nil },
				LookPath: func(n string) (string, error) { return "/bin/" + n, nil },
				Run: func(n string, a ...string) (string, error) {
					ran = append(ran, n+" "+strings.Join(a, " "))
					return "", nil
				},
				Now: time.Now,
				Out: &out,
			}
			pick := func([]pluginstall.Target) ([]pluginstall.Target, error) { return tt.pickRet, tt.pickErr }
			code := runPluginInstall(tt.args, &out, &out, d, tt.interactive, pick)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d\n%s", code, tt.wantCode, out.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output missing %q:\n%s", tt.wantOut, out.String())
			}
			if len(ran) != 0 {
				t.Errorf("ran commands without consent: %q", ran)
			}
		})
	}
}
