package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestPlanRexSetup pins the parsing of `afk rex setup`. Load-bearing rows: the
// REX_CONFIG row (the folder is the directory of init.lua, never init.lua
// itself, so the owner's file cannot be the write target) and the --print rows
// (print must name no folder, so nothing can be written). The missing-hub row
// guards the rule that --hub has no default.
func TestPlanRexSetup(t *testing.T) {
	envOf := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name    string
		args    []string
		env     map[string]string
		want    rexSetup
		wantErr string
	}{
		{"missing_hub", []string{}, nil, rexSetup{}, "--hub is needed"},
		{"bad_hub_label", []string{"--hub", "-x"}, nil, rexSetup{}, "--hub"},
		{"print_without_hub", []string{"--print"}, nil, rexSetup{}, "--hub is needed"},
		{"print_with_hub", []string{"--hub", "vm", "--print"}, map[string]string{"HOME": "/h"}, rexSetup{hub: "vm", print: true}, ""},
		{"rex_config_set", []string{"--hub", "vm"}, map[string]string{"REX_CONFIG": "/s/init.lua", "HOME": "/h"}, rexSetup{hub: "vm", dir: "/s"}, ""},
		{"rex_config_unset", []string{"--hub", "vm"}, map[string]string{"HOME": "/h"}, rexSetup{hub: "vm", dir: "/h/.config/rex"}, ""},
		{"no_home_no_config", []string{"--hub", "vm"}, nil, rexSetup{}, "home folder"},
		{"unknown_flag", []string{"--hub", "vm", "--nope"}, nil, rexSetup{}, "nope"},
		{"stray_argument", []string{"--hub", "vm", "extra"}, nil, rexSetup{}, "unexpected argument"},
		{"help", []string{"--help"}, nil, rexSetup{help: true}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := planRexSetup(c.args, envOf(c.env))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestRunRexSetup drives the command with a fake writer. Load-bearing rows: the
// write only ever goes to afk.lua's folder via the writer (init.lua is never
// opened here at all), and a write error is exit 1, not 2.
func TestRunRexSetup(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		env       map[string]string
		writeErr  error
		wantCode  int
		wantOut   string
		wantErr   string
		wantWrite string
	}{
		{"setup_writes", []string{"setup", "--hub", "vm"}, map[string]string{"REX_CONFIG": "/s/init.lua"}, nil, 0, `require("afk")`, "", "/s"},
		{"print_writes_nothing", []string{"setup", "--hub", "vm", "--print"}, nil, nil, 0, "afk_control_room", "", ""},
		{"missing_hub", []string{"setup"}, nil, nil, 2, "", "--hub is needed", ""},
		{"write_error", []string{"setup", "--hub", "vm"}, map[string]string{"REX_CONFIG": "/s/init.lua"}, errors.New("disk full"), 1, "", "disk full", "/s"},
		{"unknown_command", []string{"nope"}, nil, nil, 2, "", "unknown command", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			wrote := ""
			w := func(dir string, data []byte) (string, error) {
				wrote = dir
				if c.writeErr != nil {
					return "", c.writeErr
				}
				return dir + "/afk.lua", nil
			}
			code := runRex(c.args, &out, &errb, func(k string) string { return c.env[k] }, w)
			if code != c.wantCode {
				t.Fatalf("code = %d, want %d (stderr %q)", code, c.wantCode, errb.String())
			}
			if !strings.Contains(out.String(), c.wantOut) || !strings.Contains(errb.String(), c.wantErr) {
				t.Errorf("stdout %q stderr %q", out.String(), errb.String())
			}
			if wrote != c.wantWrite {
				t.Errorf("wrote to %q, want %q", wrote, c.wantWrite)
			}
		})
	}
}
