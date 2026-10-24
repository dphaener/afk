package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/afkstatus"
	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/projectrows"
)

func statusRow(name, task string) projectrows.Row {
	return projectrows.Row{Name: name, State: projectrows.StateRunning, Task: task, Title: "Do " + task, Phase: "build"}
}

// TestDaemonStatusReporter: the reporter sends on change and clears on stop.
// Load-bearing rows: "unchanged pass" (a quiet daemon must not write every
// pass, or the terminal is spammed), "change sends only the changed" (the
// Diff, not a full resend) and "clear with nothing sent" (a daemon that never
// reported must leave the terminal alone).
func TestDaemonStatusReporter(t *testing.T) {
	a1, a2, b1 := statusRow("a", "T1"), statusRow("a", "T2"), statusRow("b", "T1")
	rowsAB1 := []projectrows.Row{a1, b1}
	rowsAB2 := []projectrows.Row{a2, b1}

	tests := []struct {
		name  string
		steps []string // "check:<n>" selects the rows set, "clear" clears
		sets  [][]projectrows.Row
		// want is the bytes each step must write, in order.
		want func() [][]byte
	}{
		{
			name:  "first pass writes the records",
			steps: []string{"check"},
			sets:  [][]projectrows.Row{rowsAB1},
			want:  func() [][]byte { return [][]byte{afkstatus.Diff(nil, afkstatus.Records(rowsAB1))} },
		},
		{
			name:  "unchanged pass writes nothing",
			steps: []string{"check", "check"},
			sets:  [][]projectrows.Row{rowsAB1, rowsAB1},
			want:  func() [][]byte { return [][]byte{afkstatus.Diff(nil, afkstatus.Records(rowsAB1)), nil} },
		},
		{
			name:  "change sends only the changed records",
			steps: []string{"check", "check"},
			sets:  [][]projectrows.Row{rowsAB1, rowsAB2},
			want: func() [][]byte {
				return [][]byte{
					afkstatus.Diff(nil, afkstatus.Records(rowsAB1)),
					afkstatus.Diff(afkstatus.Records(rowsAB1), afkstatus.Records(rowsAB2)),
				}
			},
		},
		{
			name:  "clear after a send writes the clear",
			steps: []string{"check", "clear"},
			sets:  [][]projectrows.Row{rowsAB1},
			want: func() [][]byte {
				return [][]byte{afkstatus.Diff(nil, afkstatus.Records(rowsAB1)), afkstatus.ClearAll()}
			},
		},
		{
			name:  "clear with nothing sent writes nothing",
			steps: []string{"clear"},
			want:  func() [][]byte { return [][]byte{nil} },
		},
		{
			name:  "a second clear writes nothing",
			steps: []string{"check", "clear", "clear"},
			sets:  [][]projectrows.Row{rowsAB1},
			want: func() [][]byte {
				return [][]byte{afkstatus.Diff(nil, afkstatus.Records(rowsAB1)), afkstatus.ClearAll(), nil}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			set := 0
			r := &daemonStatusReporter{rows: func() []projectrows.Row { return tt.sets[set-1] }, out: &buf}
			want := tt.want()
			for i, step := range tt.steps {
				buf.Reset()
				if step == "check" {
					set++
					r.check(context.Background())
				} else {
					r.clear()
				}
				if !bytes.Equal(buf.Bytes(), want[i]) {
					t.Errorf("step %d (%s) wrote %q, want %q", i, step, buf.Bytes(), want[i])
				}
			}
		})
	}
}

// TestDaemonStatusAttach: the wiring decision. Load-bearing rows: "nil
// statusOut" and "writer returns nil" (no terminal, so deps.PlanWaits must be
// untouched and no bytes sent) and "keeps the old hook" (the planning hooks
// must still run).
func TestDaemonStatusAttach(t *testing.T) {
	tests := []struct {
		name    string
		out     func() io.Writer
		old     bool
		wantSet bool
	}{
		{"nil statusOut", nil, false, false},
		{"nil statusOut keeps the old hook alone", nil, true, true},
		{"writer returns nil", func() io.Writer { return nil }, false, false},
		{"terminal, no old hook", nil, false, true},
		{"terminal keeps the old hook", nil, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			env := daemonEnv{statusOut: tt.out, now: daemonTestNowFunc}
			if tt.name == "terminal, no old hook" || tt.name == "terminal keeps the old hook" {
				env.statusOut = func() io.Writer { return &buf }
			}
			oldRan := 0
			deps := daemon.Deps{}
			if tt.old {
				deps.PlanWaits = func(context.Context) { oldRan++ }
			}
			clear := attachDaemonStatus(env, &deps, func() []daemon.Project { return nil }, t.TempDir())
			if (deps.PlanWaits != nil) != tt.wantSet {
				t.Fatalf("PlanWaits set = %v, want %v", deps.PlanWaits != nil, tt.wantSet)
			}
			if deps.PlanWaits != nil {
				deps.PlanWaits(context.Background())
			}
			clear()
			terminal := env.statusOut != nil && env.statusOut() != nil
			if tt.old && oldRan != 1 {
				t.Errorf("the old hook ran %d times, want 1", oldRan)
			}
			if !terminal && buf.Len() != 0 {
				t.Errorf("no terminal but %q was written", buf.Bytes())
			}
			if terminal && buf.Len() == 0 {
				t.Error("a terminal got nothing from the first pass")
			}
		})
	}
}

func daemonTestNowFunc() time.Time { return daemonTestNow }

// TestDaemonStatusOnceWritesNothing: `afk daemon --once` returns before the
// reporter is attached, so a terminal writer sees no bytes even with a
// project to report.
func TestDaemonStatusOnceWritesNothing(t *testing.T) {
	r := newDaemonRig(t)
	root := r.project()
	r.writeConfig(fmt.Sprintf("[daemon]\ntick = \"1s\"\n[[project]]\nname = \"demo\"\npath = %q\n", root))
	var term bytes.Buffer
	env := r.env()
	env.statusOut = func() io.Writer { return &term }
	var so, se bytes.Buffer
	if code := runDaemon([]string{"--once"}, &so, &se, env); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if term.Len() != 0 || bytes.Contains(so.Bytes(), []byte("\x1b]7501")) || bytes.Contains(se.Bytes(), []byte("\x1b]7501")) {
		t.Errorf("--once wrote status bytes: term %q stdout %q stderr %q", term.Bytes(), so.Bytes(), se.Bytes())
	}
}
