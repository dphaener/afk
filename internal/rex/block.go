package rex

import (
	"context"
	"errors"
	"fmt"
)

// TerminalFlavor is the block flavor that runs a program in a terminal.
const TerminalFlavor = "com.superlogical.terminal.shell"

// formatMaxBytes is the largest part Rex hands back in one reply (960 KiB;
// a larger max_bytes is lowered to it). Sending it on every format call is
// what keeps a long scrollback from failing: with max_bytes left out, an
// output over 1 MiB fails with too_large (413) instead of paging (T17.1).
const formatMaxBytes = 983040

// Size is a terminal size in character cells. The zero Size means "Rex's
// default", which is 80x24.
type Size struct {
	Columns int
	Rows    int
}

// Terminal is a window made by CreateTerminal and the one terminal block in it.
type Terminal struct {
	WindowID string
	BlockID  string
}

// Block is one block of a session as session.list_blocks reports it.
// Placement is "placed" or "detached"; Rex says to treat any other value as
// detached, and that is left to the caller. WindowID and Owner are empty for
// a detached block and a block nobody owns.
type Block struct {
	ID        string
	Label     string
	Flavor    string
	Placement string
	WindowID  string
	Owner     string
}

// StatusRecord is one OSC 7501 record a program in a terminal reported, as
// program_status lists it. The root record has an empty ID. Progress is nil
// when the program gave none, and Owner is nil when Rex could not tell which
// process reported. A reader names the agent by App, never by Owner.Name:
// for Claude Code the owner's name is its version number (T17.1, measured).
type StatusRecord struct {
	ID           string
	App          string
	EffectiveApp string
	State        string
	Kind         string
	Msg          string
	Title        string
	Progress     *int
	UpdatedAt    string
	Owner        *StatusOwner
}

// StatusOwner is the foreground process when a status report arrived. Rex
// says to compare Name as well as PID, because PIDs are reused.
type StatusOwner struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
}

// Proc is one process in a terminal, as the process block method describes it.
type Proc struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	Argv0 string `json:"argv0"`
	Path  string `json:"path"`
	Cwd   string `json:"cwd"`
}

// ProcessExit is how a terminal's program exited.
type ProcessExit struct {
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error"`
}

// ProcessInfo is the process block method's answer. Child is the program the
// terminal started and Foreground the one in front; either is nil when Rex
// could not read it. LastExit is nil while the program still runs.
type ProcessInfo struct {
	Child      *Proc
	Foreground *Proc
	LastExit   *ProcessExit
}

// CreateTerminal opens a window labelled label in a session, holding one
// terminal block (also labelled label) that runs command in cwd.
//
// command is a program and its arguments, passed to Rex unchanged, because
// Rex's terminal takes an argument list rather than a command line, and a
// list keeps afk from having to quote anything. The block starts through the
// account's login shell (Rex's default), so the program is found on the PATH
// the owner's startup files set.
//
// The size is set at creation through options.size, never by a resize
// afterwards: a block made by a one-shot `rex api call` keeps that dead
// connection as its owner, and a resize from anyone else then answers {} and
// does nothing (T17.1). A zero Size leaves options.size out, so Rex starts
// the terminal at 80x24.
func CreateTerminal(ctx context.Context, c Caller, sessionID, label string, command []string, cwd string, size Size) (Terminal, error) {
	if sessionID == "" {
		return Terminal{}, errors.New("Rex session id is empty; afk cannot open a window without it")
	}
	if len(command) == 0 || command[0] == "" {
		return Terminal{}, errors.New("no command to run; afk cannot open a Rex terminal without one")
	}
	options := map[string]any{"command": command}
	if cwd != "" {
		options["cwd"] = cwd
	}
	if size != (Size{}) {
		// Rex takes 1 to 65535 for each; a value outside that is an afk bug,
		// refused here so it shows up as an error rather than a bad_request
		// with no hint of which number was wrong.
		if size.Columns < 1 || size.Columns > 65535 || size.Rows < 1 || size.Rows > 65535 {
			return Terminal{}, fmt.Errorf("terminal size %dx%d is out of range; columns and rows must each be 1 to 65535", size.Columns, size.Rows)
		}
		options["size"] = map[string]int{"columns": size.Columns, "rows": size.Rows}
	}
	block := map[string]any{"flavor": TerminalFlavor, "options": options}
	if label != "" {
		block["label"] = label
	}
	args := map[string]any{
		"session_id": sessionID,
		"layout":     map[string]any{"block": block},
	}
	if label != "" {
		args["window_label"] = label
	}
	raw, err := c.Call(ctx, "session.new_window", args)
	if err != nil {
		return Terminal{}, err
	}
	var reply struct {
		WindowID string   `json:"window_id"`
		BlockIDs []string `json:"block_ids"`
	}
	if err := decodeLoose("session.new_window", raw, &reply); err != nil {
		return Terminal{}, err
	}
	if reply.WindowID == "" || len(reply.BlockIDs) != 1 || reply.BlockIDs[0] == "" {
		return Terminal{}, fmt.Errorf("Rex call session.new_window answered without one window and one block (window %q, blocks %q)", reply.WindowID, reply.BlockIDs)
	}
	return Terminal{WindowID: reply.WindowID, BlockID: reply.BlockIDs[0]}, nil
}

// Theme is a terminal's default colours. A field left empty is left out of
// the request, so Rex keeps its built-in colour for it. Colours are hex,
// RRGGBB or #RRGGBB; Scheme is "light" or "dark".
type Theme struct {
	Foreground string
	Background string
	Cursor     string
	Scheme     string
}

// TerminalOptions is what CreateWindow needs to start one terminal window.
//
// Size is set at creation and never changed afterwards: a block made by a
// one-shot `rex api call` keeps that dead connection as its owner, and a
// resize from anyone else is then blocked (T17.1). A zero Size leaves it out,
// so Rex starts the terminal at 80x24. There is no environment field: the
// shell flavor takes none, so a caller that needs variables puts `env
// NAME=value` at the front of Command.
type TerminalOptions struct {
	Command []string
	Cwd     string
	Size    Size
	Theme   Theme
}

// Window is a window made by CreateWindow and the one terminal block in it.
type Window struct {
	WindowID string
	BlockID  string
}

// CreateWindow opens a window labelled label in a session, holding one shell
// block that runs t.Command directly (shell "none", so no startup file reads
// it first and the argument list reaches the program unchanged). The block
// does not exit on completion: a window whose program ended stays, and the
// host closes it itself unless the session failed, so a failed session keeps
// its last screen for the owner to read.
func CreateWindow(ctx context.Context, c Caller, sessionID, label string, t TerminalOptions) (Window, error) {
	if sessionID == "" {
		return Window{}, errors.New("Rex session id is empty; afk cannot open a window without it")
	}
	if len(t.Command) == 0 || t.Command[0] == "" {
		return Window{}, errors.New("no command to run; afk cannot open a Rex window without one")
	}
	options := map[string]any{
		"command": t.Command,
		"shell":   "none",
		"exit":    map[string]any{"on_completion": false},
	}
	if t.Cwd != "" {
		options["cwd"] = t.Cwd
	}
	if t.Size != (Size{}) {
		if t.Size.Columns < 1 || t.Size.Columns > 65535 || t.Size.Rows < 1 || t.Size.Rows > 65535 {
			return Window{}, fmt.Errorf("terminal size %dx%d is out of range; columns and rows must each be 1 to 65535", t.Size.Columns, t.Size.Rows)
		}
		options["size"] = map[string]int{"columns": t.Size.Columns, "rows": t.Size.Rows}
	}
	theme := map[string]string{}
	for k, v := range map[string]string{"foreground": t.Theme.Foreground, "background": t.Theme.Background, "cursor": t.Theme.Cursor, "scheme": t.Theme.Scheme} {
		if v != "" {
			theme[k] = v
		}
	}
	if len(theme) > 0 {
		options["theme"] = theme
	}
	block := map[string]any{"flavor": TerminalFlavor, "options": options}
	if label != "" {
		block["label"] = label
	}
	args := map[string]any{
		"session_id": sessionID,
		"layout":     map[string]any{"block": block},
	}
	if label != "" {
		args["window_label"] = label
	}
	raw, err := c.Call(ctx, "session.new_window", args)
	if err != nil {
		return Window{}, err
	}
	var reply struct {
		WindowID string   `json:"window_id"`
		BlockIDs []string `json:"block_ids"`
	}
	if err := decodeLoose("session.new_window", raw, &reply); err != nil {
		return Window{}, err
	}
	if reply.WindowID == "" || len(reply.BlockIDs) != 1 || reply.BlockIDs[0] == "" {
		return Window{}, fmt.Errorf("Rex call session.new_window answered without one window and one block (window %q, blocks %q)", reply.WindowID, reply.BlockIDs)
	}
	return Window{WindowID: reply.WindowID, BlockID: reply.BlockIDs[0]}, nil
}

// SetWindowLabel renames a window. An empty label makes Rex restore a default
// made from the window's id.
func SetWindowLabel(ctx context.Context, c Caller, sessionID, windowID, label string) error {
	if sessionID == "" || windowID == "" {
		return errors.New("Rex session or window id is empty; afk cannot relabel a window without both")
	}
	_, err := c.Call(ctx, "session.set_window_label", map[string]string{
		"session_id": sessionID, "window_id": windowID, "label": label,
	})
	return err
}

// CloseWindow closes a window and every block in it. The window id is
// required: Rex closes the active window when it is left out, which is never
// what afk means.
func CloseWindow(ctx context.Context, c Caller, sessionID, windowID string) error {
	if sessionID == "" || windowID == "" {
		return errors.New("Rex session or window id is empty; afk cannot close a window without both")
	}
	_, err := c.Call(ctx, "session.close_window", map[string]string{
		"session_id": sessionID, "window_id": windowID,
	})
	return err
}

// CloseBlock closes one block and ends the program in it.
func CloseBlock(ctx context.Context, c Caller, sessionID, blockID string) error {
	if err := checkBlockIDs(sessionID, blockID); err != nil {
		return err
	}
	_, err := c.Call(ctx, "block.close", map[string]string{
		"session_id": sessionID, "block_id": blockID,
	})
	return err
}

// WindowOfBlock finds the window a block sits in, from session.list_blocks.
// found is false, with no error, when the block is not listed or is detached
// (it has no window).
func WindowOfBlock(ctx context.Context, c Caller, sessionID, blockID string) (string, bool, error) {
	if err := checkBlockIDs(sessionID, blockID); err != nil {
		return "", false, err
	}
	blocks, err := ListBlocks(ctx, c, sessionID)
	if err != nil {
		return "", false, err
	}
	for _, b := range blocks {
		if b.ID == blockID {
			return b.WindowID, b.WindowID != "", nil
		}
	}
	return "", false, nil
}

// SetBlockLabel renames a block. An empty label makes Rex restore a default
// made from the block's id.
func SetBlockLabel(ctx context.Context, c Caller, sessionID, blockID, label string) error {
	if err := checkBlockIDs(sessionID, blockID); err != nil {
		return err
	}
	_, err := c.Call(ctx, "session.set_block_label", map[string]string{
		"session_id": sessionID, "block_id": blockID, "label": label,
	})
	return err
}

// ListBlocks lists every block in a session, detached ones included, sorted
// by block id as Rex returns them.
func ListBlocks(ctx context.Context, c Caller, sessionID string) ([]Block, error) {
	if sessionID == "" {
		return nil, errors.New("Rex session id is empty; afk cannot list its blocks without it")
	}
	raw, err := c.Call(ctx, "session.list_blocks", map[string]string{"session_id": sessionID})
	if err != nil {
		return nil, err
	}
	var reply struct {
		Blocks []struct {
			BlockID   string `json:"block_id"`
			Label     string `json:"label"`
			Flavor    string `json:"flavor"`
			Placement string `json:"placement"`
			WindowID  string `json:"window_id"`
			Owner     string `json:"owner"`
		} `json:"blocks"`
	}
	if err := decodeLoose("session.list_blocks", raw, &reply); err != nil {
		return nil, err
	}
	out := make([]Block, 0, len(reply.Blocks))
	for _, b := range reply.Blocks {
		out = append(out, Block{ID: b.BlockID, Label: b.Label, Flavor: b.Flavor, Placement: b.Placement, WindowID: b.WindowID, Owner: b.Owner})
	}
	return out, nil
}

// ProgramStatus lists the OSC 7501 records programs in a block reported,
// root first. No record is an empty slice, never an error: a terminal whose
// program has not reported yet (Claude Code behind its trust dialog, T17.1)
// answers {"records": []}.
func ProgramStatus(ctx context.Context, c Caller, sessionID, blockID string) ([]StatusRecord, error) {
	var reply struct {
		Records []struct {
			ID           string       `json:"id"`
			App          string       `json:"app"`
			EffectiveApp string       `json:"effective_app"`
			State        string       `json:"state"`
			Kind         string       `json:"kind"`
			Msg          string       `json:"msg"`
			Title        string       `json:"title"`
			Progress     *int         `json:"progress"`
			UpdatedAt    string       `json:"updated_at"`
			Owner        *StatusOwner `json:"owner"`
		} `json:"records"`
	}
	if err := callBlockMethod(ctx, c, sessionID, blockID, "program_status", nil, &reply); err != nil {
		return nil, err
	}
	out := make([]StatusRecord, 0, len(reply.Records))
	for _, r := range reply.Records {
		out = append(out, StatusRecord{
			ID: r.ID, App: r.App, EffectiveApp: r.EffectiveApp, State: r.State, Kind: r.Kind,
			Msg: r.Msg, Title: r.Title, Progress: r.Progress, UpdatedAt: r.UpdatedAt, Owner: r.Owner,
		})
	}
	return out, nil
}

// Process reads the processes in a block's terminal and how its program
// exited.
func Process(ctx context.Context, c Caller, sessionID, blockID string) (ProcessInfo, error) {
	var reply struct {
		Child      *Proc        `json:"child"`
		Foreground *Proc        `json:"foreground"`
		LastExit   *ProcessExit `json:"last_exit"`
	}
	if err := callBlockMethod(ctx, c, sessionID, blockID, "process", nil, &reply); err != nil {
		return ProcessInfo{}, err
	}
	return ProcessInfo{Child: reply.Child, Foreground: reply.Foreground, LastExit: reply.LastExit}, nil
}

// Format returns a block's screen and its whole scrollback in format ("text",
// "html" or "vt"; empty is Rex's default, text). Rex has no screen-only
// option (T17.1).
//
// max_bytes is always sent, because without it an output over 1 MiB fails
// with too_large (413). With it, Rex answers the first part and, when more
// remains, an id and the total length; Format then reads the rest with
// blob_read from the bytes it holds so far until it has total bytes, and
// returns the whole output. A blob_read part can be shorter than asked even
// when more remains (Rex's own note), so the next offset is always the bytes
// actually received, never a fixed step.
func Format(ctx context.Context, c Caller, sessionID, blockID, format string) ([]byte, error) {
	args := map[string]any{"max_bytes": formatMaxBytes}
	if format != "" {
		args["format"] = format
	}
	var first struct {
		Content string `json:"content"`
		ID      *int64 `json:"id"`
		Total   *int64 `json:"total"`
	}
	if err := callBlockMethod(ctx, c, sessionID, blockID, "format", args, &first); err != nil {
		return nil, err
	}
	got := []byte(first.Content)
	if first.ID == nil {
		// No id means this reply is the whole output.
		return got, nil
	}
	if first.Total == nil {
		return nil, errors.New("Rex call format said more output remains but gave no total length")
	}
	id, total := *first.ID, *first.Total
	for int64(len(got)) < total {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var part struct {
			Data  string `json:"data"`
			Total int64  `json:"total"`
			EOF   bool   `json:"eof"`
		}
		readArgs := map[string]any{"id": id, "offset": len(got), "max_bytes": formatMaxBytes}
		if err := callBlockMethod(ctx, c, sessionID, blockID, "blob_read", readArgs, &part); err != nil {
			return nil, err
		}
		if part.Total != total {
			// A different total means a different result than the first part
			// came from; splicing the two would hand back a corrupt screen.
			return nil, fmt.Errorf("Rex blob_read for format output %d changed length from %d to %d bytes while it was being read", id, total, part.Total)
		}
		if part.Data == "" {
			// No progress: looping again would ask for the same offset forever.
			return nil, fmt.Errorf("Rex blob_read for format output %d stopped at %d of %d bytes", id, len(got), total)
		}
		got = append(got, part.Data...)
		if part.EOF && int64(len(got)) < total {
			return nil, fmt.Errorf("Rex blob_read for format output %d ended at %d of %d bytes", id, len(got), total)
		}
	}
	if int64(len(got)) != total {
		return nil, fmt.Errorf("Rex format output %d came to %d bytes, more than the %d it announced", id, len(got), total)
	}
	return got, nil
}

// ScreenSize is what FormatSize learned about a block's output. Total is the
// whole output's length in bytes. Content is the first part Rex sent back,
// which is the whole output when Total is no more than the max_bytes asked.
type ScreenSize struct {
	Total   int64
	Content []byte
}

// FormatSize learns how big a block's formatted output is without reading all
// of it. T17.1 measured that a format call with a small max_bytes answers with
// the first part and a total, so the web page's watch view can read the size
// first and skip the scrollback (about 0.9 MB of HTML for a busy block) when
// it is large. Unlike Format it never calls blob_read: when Rex says more
// remains, Content is only the first part and Total is the announced length.
// A reply with no id is the whole output, so Total is its length. Errors are
// the ones Format returns for the same first call.
func FormatSize(ctx context.Context, c Caller, sessionID, blockID, format string, maxBytes int64) (ScreenSize, error) {
	args := map[string]any{"max_bytes": maxBytes}
	if format != "" {
		args["format"] = format
	}
	var first struct {
		Content string `json:"content"`
		ID      *int64 `json:"id"`
		Total   *int64 `json:"total"`
	}
	if err := callBlockMethod(ctx, c, sessionID, blockID, "format", args, &first); err != nil {
		return ScreenSize{}, err
	}
	content := []byte(first.Content)
	if first.ID == nil {
		return ScreenSize{Total: int64(len(content)), Content: content}, nil
	}
	if first.Total == nil {
		return ScreenSize{}, errors.New("Rex call format said more output remains but gave no total length")
	}
	return ScreenSize{Total: *first.Total, Content: content}, nil
}

// callBlockMethod calls one of a terminal block's own methods through
// block.method and decodes its answer into reply. The method is the short
// name (program_status, format, …), never the long
// com.superlogical.terminal.… one: T17.1 measured method_not_found for the
// long name in block.method's method field. session_id and block_id go at the top and
// the method's own arguments under args, as block.method's schema says.
func callBlockMethod(ctx context.Context, c Caller, sessionID, blockID, method string, args map[string]any, reply any) error {
	if err := checkBlockIDs(sessionID, blockID); err != nil {
		return err
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.Call(ctx, "block.method", map[string]any{
		"session_id": sessionID,
		"block_id":   blockID,
		"method":     method,
		"args":       args,
	})
	if err != nil {
		return err
	}
	return decodeLoose("block "+method, raw, reply)
}

// checkBlockIDs refuses an empty session or block id before any call: Rex
// would answer bad_request, and an empty id here is always an afk bug, so it
// is better named in afk's words.
func checkBlockIDs(sessionID, blockID string) error {
	if sessionID == "" || blockID == "" {
		return errors.New("Rex session or block id is empty; afk cannot reach a block without both")
	}
	return nil
}
