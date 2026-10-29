package rex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// blockArgs is the block.method request a block call should send: the ids at
// the top, the short method name, and the method's own arguments under args.
func blockArgs(method string, args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	return map[string]any{"session_id": "session:s1", "block_id": "block:b1", "method": method, "args": args}
}

func blobArgs(id, offset int) map[string]any {
	return blockArgs("blob_read", map[string]any{"id": id, "offset": offset, "max_bytes": formatMaxBytes})
}

var formatArgs = blockArgs("format", map[string]any{"format": "html", "max_bytes": formatMaxBytes})

func sessionNotFound(method string) error {
	return &CallError{Method: method, Code: 3, Stderr: "session session:s1 not found (status=404 code=session_not_found)"}
}

// mustJSON marshals v the way Fake records args, so a test compares the
// request byte for byte.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestFormat: paged and blob_short are load-bearing. paged pins the
// concatenation in order to exactly total bytes; blob_short pins that the
// next offset is the bytes actually received (Rex may return less than asked
// with more remaining), so a fixed step would skip or repeat output. Every
// row also checks the first call carries max_bytes, because format without it
// fails with too_large on a long scrollback. no_progress, total_changed and
// eof_early pin that a misbehaving server ends in an error, never a loop or a
// spliced screen.
func TestFormat(t *testing.T) {
	cases := []struct {
		name      string
		script    func(f *Fake)
		want      string
		wantErr   string
		wantCode  int
		wantCalls int
	}{
		{
			name: "whole reply without id",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, `{"content":"<pre>hi</pre>","eof":true,"total":13}`, nil)
			},
			want: "<pre>hi</pre>", wantCalls: 1,
		},
		{
			name: "paged",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, `{"content":"abcd","id":7,"total":10,"eof":false}`, nil)
				f.OnArgs("block.method", blobArgs(7, 4), `{"id":7,"data":"efg","total":10,"eof":false}`, nil)
				f.OnArgs("block.method", blobArgs(7, 7), `{"id":7,"data":"hij","total":10,"eof":true}`, nil)
			},
			want: "abcdefghij", wantCalls: 3,
		},
		{
			name: "blob_short",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, `{"content":"abc","id":2,"total":9}`, nil)
				// Asked for up to 960 KiB from offset 3, Rex returns one byte.
				f.OnArgs("block.method", blobArgs(2, 3), `{"id":2,"data":"d","total":9,"eof":false}`, nil)
				f.OnArgs("block.method", blobArgs(2, 4), `{"id":2,"data":"efghi","total":9,"eof":true}`, nil)
			},
			want: "abcdefghi", wantCalls: 3,
		},
		{
			name: "multibyte content counts bytes",
			script: func(f *Fake) {
				// "é" is two bytes, so the next offset is 3, not 2.
				f.OnArgs("block.method", formatArgs, `{"content":"aé","id":4,"total":5}`, nil)
				f.OnArgs("block.method", blobArgs(4, 3), `{"id":4,"data":"bc","total":5,"eof":true}`, nil)
			},
			want: "aébc", wantCalls: 2,
		},
		{
			name: "unknown session",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, ``, sessionNotFound("block.method"))
			},
			wantErr: "session_not_found", wantCode: 3, wantCalls: 1,
		},
		{
			name: "blob_read error returned",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, `{"content":"ab","id":1,"total":4}`, nil)
				f.OnArgs("block.method", blobArgs(1, 2), ``, &CallError{Method: "block.method", Code: 3, Stderr: "blob 1 not found (status=404 code=not_found)"})
			},
			wantErr: "code=not_found", wantCode: 3, wantCalls: 2,
		},
		{
			name: "no progress",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, `{"content":"ab","id":1,"total":4}`, nil)
				f.OnArgs("block.method", blobArgs(1, 2), `{"id":1,"data":"","total":4,"eof":false}`, nil)
			},
			wantErr: "stopped at 2 of 4", wantCalls: 2,
		},
		{
			name: "total changed",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, `{"content":"ab","id":1,"total":4}`, nil)
				f.OnArgs("block.method", blobArgs(1, 2), `{"id":1,"data":"cdef","total":6,"eof":true}`, nil)
			},
			wantErr: "changed length", wantCalls: 2,
		},
		{
			name: "eof early",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, `{"content":"ab","id":1,"total":5}`, nil)
				f.OnArgs("block.method", blobArgs(1, 2), `{"id":1,"data":"c","total":5,"eof":true}`, nil)
			},
			wantErr: "ended at 3 of 5", wantCalls: 2,
		},
		{
			name: "id without total",
			script: func(f *Fake) {
				f.OnArgs("block.method", formatArgs, `{"content":"ab","id":1}`, nil)
			},
			wantErr: "no total", wantCalls: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &Fake{}
			tc.script(f)
			got, err := Format(context.Background(), f, "session:s1", "block:b1", "html")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want text %q", err, tc.wantErr)
				}
				var ce *CallError
				if tc.wantCode != 0 && (!errors.As(err, &ce) || ce.ExitCode() != tc.wantCode) {
					t.Fatalf("err = %v, want CallError with exit %d", err, tc.wantCode)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				if string(got) != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
				t.Logf("returned %d bytes: %q", len(got), got)
			}
			calls := f.Calls()
			if len(calls) != tc.wantCalls {
				t.Fatalf("calls = %d, want %d: %+v", len(calls), tc.wantCalls, calls)
			}
			if string(calls[0].Args) != mustJSON(t, formatArgs) {
				t.Fatalf("first call args %s, want max_bytes %d sent", calls[0].Args, formatMaxBytes)
			}
		})
	}
}

// TestFormatDefaultFormat pins that an empty format leaves the field out
// (Rex's default, text) but still sends max_bytes.
func TestFormatDefaultFormat(t *testing.T) {
	want := blockArgs("format", map[string]any{"max_bytes": formatMaxBytes})
	f := (&Fake{}).OnArgs("block.method", want, `{"content":"x"}`, nil)
	got, err := Format(context.Background(), f, "session:s1", "block:b1", "")
	if err != nil || string(got) != "x" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestProgramStatus: empty_records is load-bearing (a block whose program has
// not reported must read as no records, never an error, or every new agent
// would look broken until its first report); with_records pins the decode of
// progress and owner, and an unknown field is ignored because Rex adds fields.
func TestProgramStatus(t *testing.T) {
	psArgs := blockArgs("program_status", nil)
	progress := 40
	cases := []struct {
		name     string
		reply    string
		callErr  error
		session  string
		want     []StatusRecord
		wantErr  string
		wantCode int
		noCalls  bool
	}{
		{
			name: "with records",
			reply: `{"records":[
				{"state":"working","app":"claude-code","effective_app":"claude-code","msg":"thinking","updated_at":"2026-10-28T10:00:00Z","owner":{"pid":42,"name":"2.1.311"},"future":1},
				{"id":"afk","state":"blocked","kind":"question","app":"afk","progress":40,"title":"t","updated_at":"2026-10-28T10:00:01Z"}]}`,
			session: "session:s1",
			want: []StatusRecord{
				{State: "working", App: "claude-code", EffectiveApp: "claude-code", Msg: "thinking", UpdatedAt: "2026-10-28T10:00:00Z", Owner: &StatusOwner{PID: 42, Name: "2.1.311"}},
				{ID: "afk", State: "blocked", Kind: "question", App: "afk", Progress: &progress, Title: "t", UpdatedAt: "2026-10-28T10:00:01Z"},
			},
		},
		{name: "empty records", reply: `{"records":[]}`, session: "session:s1", want: []StatusRecord{}},
		{name: "unknown session", callErr: sessionNotFound("block.method"), session: "session:s1", wantErr: "session_not_found", wantCode: 3},
		{name: "not json", reply: `nope`, session: "session:s1", wantErr: "could not read"},
		{name: "empty session id refused", session: "", wantErr: "id is empty", noCalls: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := (&Fake{}).OnArgs("block.method", psArgs, tc.reply, tc.callErr)
			got, err := ProgramStatus(context.Background(), f, tc.session, "block:b1")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want text %q", err, tc.wantErr)
				}
				var ce *CallError
				if tc.wantCode != 0 && (!errors.As(err, &ce) || ce.ExitCode() != tc.wantCode) {
					t.Fatalf("err = %v, want CallError exit %d", err, tc.wantCode)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				if got == nil {
					t.Fatal("records are nil, want a non-nil slice")
				}
				if mustJSON(t, got) != mustJSON(t, tc.want) {
					t.Fatalf("got %s, want %s", mustJSON(t, got), mustJSON(t, tc.want))
				}
			}
			if n := len(f.Calls()); (n == 0) != tc.noCalls {
				t.Fatalf("calls = %d, noCalls %v", n, tc.noCalls)
			}
		})
	}
}

// TestProcess pins the decode of the child, the foreground program and the
// exit, and that a running program reads as a nil LastExit.
func TestProcess(t *testing.T) {
	pArgs := blockArgs("process", nil)
	cases := []struct {
		name     string
		reply    string
		wantFG   string
		wantPID  int
		wantExit *int
	}{
		{
			name:    "running",
			reply:   `{"child":{"pid":10,"user_id":1000,"name":"afk"},"foreground":{"pid":11,"user_id":1000,"name":"claude","argv0":"claude","cwd":"/w"},"last_exit":null,"home":"/h"}`,
			wantFG:  "claude",
			wantPID: 11,
		},
		{
			name:     "exited",
			reply:    `{"last_exit":{"exit_code":2,"error":"exit status 2"}}`,
			wantExit: func() *int { n := 2; return &n }(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := (&Fake{}).OnArgs("block.method", pArgs, tc.reply, nil)
			got, err := Process(context.Background(), f, "session:s1", "block:b1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantFG != "" && (got.Foreground == nil || got.Foreground.Name != tc.wantFG || got.Foreground.PID != tc.wantPID) {
				t.Fatalf("foreground = %+v", got.Foreground)
			}
			if tc.wantFG == "" && got.Foreground != nil {
				t.Fatalf("foreground = %+v, want nil", got.Foreground)
			}
			if (tc.wantExit == nil) != (got.LastExit == nil) || (tc.wantExit != nil && got.LastExit.ExitCode != *tc.wantExit) {
				t.Fatalf("last exit = %+v", got.LastExit)
			}
		})
	}
}

// TestCreateTerminal: with_size is load-bearing, because the size must go in
// options.size at creation (a later resize from afk's one-shot connection does
// nothing, T17.1). The refusal rows pin that a bad request never reaches Rex.
func TestCreateTerminal(t *testing.T) {
	cases := []struct {
		name     string
		command  []string
		size     Size
		session  string
		wantArgs map[string]any
		reply    string
		want     Terminal
		wantErr  string
	}{
		{
			name: "with size", session: "session:s1", command: []string{"afk", "_host", "--stdio"}, size: Size{Columns: 120, Rows: 40},
			wantArgs: map[string]any{
				"session_id": "session:s1", "window_label": "w",
				"layout": map[string]any{"block": map[string]any{
					"flavor": TerminalFlavor, "label": "w",
					"options": map[string]any{"command": []string{"afk", "_host", "--stdio"}, "cwd": "/repo", "size": map[string]int{"columns": 120, "rows": 40}},
				}},
			},
			reply: `{"window_id":"window:w1","block_ids":["block:b1"],"revision":3}`,
			want:  Terminal{WindowID: "window:w1", BlockID: "block:b1"},
		},
		{
			name: "zero size left out", session: "session:s1", command: []string{"sh"},
			wantArgs: map[string]any{
				"session_id": "session:s1", "window_label": "w",
				"layout": map[string]any{"block": map[string]any{
					"flavor": TerminalFlavor, "label": "w",
					"options": map[string]any{"command": []string{"sh"}, "cwd": "/repo"},
				}},
			},
			reply: `{"window_id":"window:w1","block_ids":["block:b1"],"revision":3}`,
			want:  Terminal{WindowID: "window:w1", BlockID: "block:b1"},
		},
		{name: "no block id in reply", session: "session:s1", command: []string{"sh"}, reply: `{"window_id":"window:w1","block_ids":[],"revision":3}`, wantErr: "one window and one block"},
		{name: "size out of range", session: "session:s1", command: []string{"sh"}, size: Size{Columns: 0, Rows: 30}, wantErr: "out of range"},
		{name: "no command", session: "session:s1", wantErr: "without one"},
		{name: "no session", command: []string{"sh"}, wantErr: "session id is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := (&Fake{}).On("session.new_window", tc.reply, nil)
			got, err := CreateTerminal(context.Background(), f, tc.session, "w", tc.command, "/repo", tc.size)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want text %q", err, tc.wantErr)
				}
				if tc.reply == "" && len(f.Calls()) != 0 {
					t.Fatalf("refused request still called Rex: %+v", f.Calls())
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v", got, err)
			}
			calls := f.Calls()
			if len(calls) != 1 || string(calls[0].Args) != mustJSON(t, tc.wantArgs) {
				t.Fatalf("calls = %+v, want args %s", calls, mustJSON(t, tc.wantArgs))
			}
		})
	}
}

// TestBlockLabelAndList pins the request shapes of set_block_label and
// list_blocks, and that list_blocks ignores a field Rex may add.
func TestBlockLabelAndList(t *testing.T) {
	t.Run("set label", func(t *testing.T) {
		want := map[string]string{"session_id": "session:s1", "block_id": "block:b1", "label": "agent"}
		f := (&Fake{}).OnArgs("session.set_block_label", want, `{"revision":4}`, nil)
		if err := SetBlockLabel(context.Background(), f, "session:s1", "block:b1", "agent"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("set label empty block refused", func(t *testing.T) {
		f := &Fake{}
		if err := SetBlockLabel(context.Background(), f, "session:s1", "", "agent"); err == nil || len(f.Calls()) != 0 {
			t.Fatalf("err %v, calls %+v", err, f.Calls())
		}
	})
	t.Run("list blocks", func(t *testing.T) {
		f := (&Fake{}).OnArgs("session.list_blocks", map[string]string{"session_id": "session:s1"},
			`{"blocks":[{"block_id":"block:b1","label":"agent","flavor":"com.superlogical.terminal.shell","placement":"placed","window_id":"window:w1","owner":"client:c1","host_id":"h","new_field":true},
			{"block_id":"block:b2","label":"x","placement":"detached"}]}`, nil)
		got, err := ListBlocks(context.Background(), f, "session:s1")
		if err != nil {
			t.Fatal(err)
		}
		want := []Block{
			{ID: "block:b1", Label: "agent", Flavor: TerminalFlavor, Placement: "placed", WindowID: "window:w1", Owner: "client:c1"},
			{ID: "block:b2", Label: "x", Placement: "detached"},
		}
		if mustJSON(t, got) != mustJSON(t, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("list blocks unknown session", func(t *testing.T) {
		f := (&Fake{}).On("session.list_blocks", ``, sessionNotFound("session.list_blocks"))
		_, err := ListBlocks(context.Background(), f, "session:s1")
		var ce *CallError
		if !errors.As(err, &ce) || ce.ExitCode() != 3 {
			t.Fatalf("err = %v", err)
		}
	})
}

func errCode(t *testing.T, err error, want int) {
	t.Helper()
	var ce *CallError
	if !errors.As(err, &ce) || ce.ExitCode() != want {
		t.Fatalf("err = %v, want a CallError with exit code %d", err, want)
	}
}

func callErr(method string, code int, status string) error {
	return &CallError{Method: method, Code: code, Stderr: status}
}

// TestCreateWindow: the request rows are load-bearing. shell none and
// exit.on_completion false are what make the host's own exit handling the
// only thing that closes a window; empty theme fields must be omitted or Rex
// rejects them or paints black.
func TestCreateWindow(t *testing.T) {
	full := TerminalOptions{
		Command: []string{"afk", "_host"}, Cwd: "/w", Size: Size{160, 48},
		Theme: Theme{Foreground: "#fff", Background: "#000", Cursor: "#ccc", Scheme: "dark"},
	}
	opts := func(extra map[string]any) map[string]any {
		o := map[string]any{"command": []string{"afk", "_host"}, "shell": "none", "exit": map[string]any{"on_completion": false}}
		for k, v := range extra {
			o[k] = v
		}
		return o
	}
	req := func(o map[string]any) map[string]any {
		return map[string]any{
			"session_id":   "session:s1",
			"window_label": "x · build",
			"layout":       map[string]any{"block": map[string]any{"flavor": TerminalFlavor, "label": "x · build", "options": o}},
		}
	}
	ok := `{"window_id":"window:w1","block_ids":["block:b1"],"revision":3}`
	tests := []struct {
		name     string
		t        TerminalOptions
		session  string
		reply    string
		err      error
		want     map[string]any
		wantErr  bool
		wantCode int
	}{
		{name: "full options", t: full, session: "session:s1", reply: ok, want: req(opts(map[string]any{
			"cwd": "/w", "size": map[string]int{"columns": 160, "rows": 48},
			"theme": map[string]string{"foreground": "#fff", "background": "#000", "cursor": "#ccc", "scheme": "dark"},
		}))},
		{name: "empty theme fields omitted", t: TerminalOptions{Command: []string{"afk", "_host"}, Theme: Theme{Scheme: "dark"}}, session: "session:s1", reply: ok,
			want: req(opts(map[string]any{"theme": map[string]string{"scheme": "dark"}}))},
		{name: "no theme no size no cwd", t: TerminalOptions{Command: []string{"afk", "_host"}}, session: "session:s1", reply: ok, want: req(opts(nil))},
		{name: "no command", t: TerminalOptions{}, session: "session:s1", wantErr: true},
		{name: "empty session", t: full, session: "", wantErr: true},
		{name: "size out of range", t: TerminalOptions{Command: []string{"a"}, Size: Size{0, 5}}, session: "session:s1", wantErr: true},
		{name: "reply without block", t: full, session: "session:s1", reply: `{"window_id":"window:w1","block_ids":[]}`, wantErr: true},
		{name: "session_not_found", t: full, session: "session:s1", err: callErr("session.new_window", 3, "session_not_found"), wantErr: true, wantCode: 3},
		{name: "bad_request", t: full, session: "session:s1", err: callErr("session.new_window", 2, "bad_request"), wantErr: true, wantCode: 2},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			f := (&Fake{}).On("session.new_window", tt.reply, tt.err)
			w, err := CreateWindow(context.Background(), f, tt.session, "x · build", tt.t)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", w)
				}
				if tt.wantCode != 0 {
					errCode(t, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w != (Window{WindowID: "window:w1", BlockID: "block:b1"}) {
				t.Errorf("window = %+v", w)
			}
			calls := f.Calls()
			if len(calls) != 1 || string(calls[0].Args) != mustJSON(t, tt.want) {
				t.Errorf("args = %s\nwant   %s", calls[0].Args, mustJSON(t, tt.want))
			}
		})
	}
}

// TestSetWindowLabel: the exact keys, plus the not_found row that must
// surface as a CallError rather than be swallowed.
func TestSetWindowLabel(t *testing.T) {
	tests := []struct {
		name     string
		session  string
		window   string
		err      error
		wantErr  bool
		wantCode int
		wantCall bool
	}{
		{name: "ok", session: "session:s1", window: "window:w1", wantCall: true},
		{name: "empty window", session: "session:s1", window: "", wantErr: true},
		{name: "not_found", session: "session:s1", window: "window:w1", err: callErr("session.set_window_label", 3, "not_found"), wantErr: true, wantCode: 3, wantCall: true},
		{name: "session_not_found", session: "session:s1", window: "window:w1", err: callErr("session.set_window_label", 3, "session_not_found"), wantErr: true, wantCode: 3, wantCall: true},
		{name: "bad_request", session: "session:s1", window: "window:w1", err: callErr("session.set_window_label", 2, "bad_request"), wantErr: true, wantCode: 2, wantCall: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Fake{}).On("session.set_window_label", `{}`, tt.err)
			err := SetWindowLabel(context.Background(), f, tt.session, tt.window, "x · fix")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantCode != 0 {
				errCode(t, err, tt.wantCode)
			}
			calls := f.Calls()
			if (len(calls) == 1) != tt.wantCall {
				t.Fatalf("calls = %d", len(calls))
			}
			if tt.wantCall {
				want := mustJSON(t, map[string]string{"session_id": tt.session, "window_id": tt.window, "label": "x · fix"})
				if string(calls[0].Args) != want {
					t.Errorf("args = %s, want %s", calls[0].Args, want)
				}
			}
		})
	}
}

// TestCloseWindow: not_found is the load-bearing row (a window closed by hand
// must come back as a CallError with exit 3 so a caller can treat it as
// already closed); an empty window id is refused because Rex would close the
// active window instead.
func TestCloseWindow(t *testing.T) {
	tests := []struct {
		name     string
		window   string
		err      error
		wantErr  bool
		wantCode int
		wantCall bool
	}{
		{name: "ok", window: "window:w1", wantCall: true},
		{name: "empty window", window: "", wantErr: true},
		{name: "not_found", window: "window:w1", err: callErr("session.close_window", 3, "window window:w1 not found (status=404 code=not_found)"), wantErr: true, wantCode: 3, wantCall: true},
		{name: "session_not_found", window: "window:w1", err: callErr("session.close_window", 3, "session_not_found"), wantErr: true, wantCode: 3, wantCall: true},
		{name: "bad_request", window: "window:w1", err: callErr("session.close_window", 2, "bad_request"), wantErr: true, wantCode: 2, wantCall: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Fake{}).On("session.close_window", `{}`, tt.err)
			err := CloseWindow(context.Background(), f, "session:s1", tt.window)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantCode != 0 {
				errCode(t, err, tt.wantCode)
			}
			calls := f.Calls()
			if (len(calls) == 1) != tt.wantCall {
				t.Fatalf("calls = %d", len(calls))
			}
			if tt.wantCall {
				want := mustJSON(t, map[string]string{"session_id": "session:s1", "window_id": tt.window})
				if string(calls[0].Args) != want {
					t.Errorf("args = %s, want %s", calls[0].Args, want)
				}
			}
		})
	}
}

// TestCloseBlock: block.close is sent with block_id only, never window_id
// (Rex says to send one or the other).
func TestCloseBlock(t *testing.T) {
	tests := []struct {
		name     string
		block    string
		err      error
		wantErr  bool
		wantCode int
		wantCall bool
	}{
		{name: "ok", block: "block:b1", wantCall: true},
		{name: "empty block", block: "", wantErr: true},
		{name: "not_found", block: "block:b1", err: callErr("block.close", 3, "not_found"), wantErr: true, wantCode: 3, wantCall: true},
		{name: "session_not_found", block: "block:b1", err: sessionNotFound("block.close"), wantErr: true, wantCode: 3, wantCall: true},
		{name: "bad_request", block: "block:b1", err: callErr("block.close", 2, "bad_request"), wantErr: true, wantCode: 2, wantCall: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Fake{}).On("block.close", `{}`, tt.err)
			err := CloseBlock(context.Background(), f, "session:s1", tt.block)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantCode != 0 {
				errCode(t, err, tt.wantCode)
			}
			calls := f.Calls()
			if (len(calls) == 1) != tt.wantCall {
				t.Fatalf("calls = %d", len(calls))
			}
			if tt.wantCall {
				want := mustJSON(t, map[string]string{"session_id": "session:s1", "block_id": tt.block})
				if string(calls[0].Args) != want {
					t.Errorf("args = %s, want %s", calls[0].Args, want)
				}
			}
		})
	}
}

// TestWindowOfBlock: detached and unlisted blocks are found=false with no
// error, so the caller can tell "no window" from "Rex failed".
func TestWindowOfBlock(t *testing.T) {
	list := `{"blocks":[{"block_id":"block:b1","placement":"placed","window_id":"window:w1"},{"block_id":"block:b2","placement":"detached"}]}`
	tests := []struct {
		name      string
		block     string
		reply     string
		err       error
		wantWin   string
		wantFound bool
		wantErr   bool
		wantCode  int
	}{
		{name: "placed", block: "block:b1", reply: list, wantWin: "window:w1", wantFound: true},
		{name: "detached", block: "block:b2", reply: list},
		{name: "not listed", block: "block:zz", reply: list},
		{name: "empty block", block: "", reply: list, wantErr: true},
		{name: "not json", block: "block:b1", reply: `nope`, wantErr: true},
		{name: "session_not_found", block: "block:b1", err: sessionNotFound("session.list_blocks"), wantErr: true, wantCode: 3},
		{name: "bad_request", block: "block:b1", err: callErr("session.list_blocks", 2, "bad_request"), wantErr: true, wantCode: 2},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			f := (&Fake{}).On("session.list_blocks", tt.reply, tt.err)
			win, found, err := WindowOfBlock(context.Background(), f, "session:s1", tt.block)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantCode != 0 {
				errCode(t, err, tt.wantCode)
			}
			if win != tt.wantWin || found != tt.wantFound {
				t.Errorf("got (%q, %v), want (%q, %v)", win, found, tt.wantWin, tt.wantFound)
			}
		})
	}
}
