package main

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
)

const (
	noticeOldPath = "/cfg/afk/config.json"
	noticeNewPath = "/cfg/afk/config.toml"
	// noticeShort is the toast text. It is the same for every old file that
	// exists: what was in the file is the terminal text's business.
	noticeShort = "afk no longer reads config.json. Quit to see the lines to add to config.toml, printed in the terminal."
)

// noticeBlock returns the lines of the notice's [dash] block: from the [dash]
// line to the blank line that ends it. ok is false when the notice has none.
func noticeBlock(full string) (block []string, ok bool) {
	lines := strings.Split(full, "\n")
	for i, line := range lines {
		if line != "[dash]" {
			continue
		}
		for _, l := range lines[i:] {
			if l == "" {
				break
			}
			block = append(block, l)
		}
		return block, true
	}
	return nil, false
}

// loadNoticeBlock reads the block the way a person who pasted it into an
// empty config.toml would have it read: through daemonconfig.Load, with the
// file served by a fake ReadFile so no disk is touched.
func loadNoticeBlock(t *testing.T, block []string) daemonconfig.Config {
	t.Helper()
	text := strings.Join(block, "\n") + "\n"
	cfg, err := daemonconfig.Load(daemonconfig.Deps{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return "/cfg"
			}
			return ""
		},
		HomeDir: func() (string, error) { return "/home/someone", nil },
		ReadFile: func(path string) ([]byte, error) {
			if path != noticeNewPath {
				return nil, fs.ErrNotExist
			}
			return []byte(text), nil
		},
	})
	if err != nil {
		t.Fatalf("the built lines do not load as config.toml: %v\n%s", err, text)
	}
	return cfg
}

// TestOldSettingsNotice is the builder's whole matrix.
//
// Load-bearing rows:
//   - "no file" is the rule the task turns on: a machine without the old file
//     must never be told about it. Both texts empty, whatever else is passed
//     (the row passes bytes and an error on purpose, so a builder that looked
//     at them before looking at exists fails).
//   - "invalid JSON" and "read error": the notice is still shown when nothing
//     can be built, because the file being there is the news, and it carries
//     the commented example instead of lines.
//   - "interval under the floor", "depth of 0" and the other refused values:
//     a value the [dash] reader would refuse is left out and named. Every row
//     with a block is then loaded through daemonconfig.Load and must come back
//     with a nil DashErr, which is what proves a pasted block can never break
//     the folder search, and what catches oldSettingsMinInterval drifting from
//     the reader's own floor.
//   - "a quote and a backslash in a root": the root has to come back from the
//     TOML reader as the same folder that was in the old file, so the quoting
//     is checked by decoding, not by comparing text.
//   - "other keys are never echoed": the old file may hold anything; only the
//     three search keys may reach the terminal.
func TestOldSettingsNotice(t *testing.T) {
	tests := []struct {
		name    string
		missing bool
		data    string
		readErr error

		wantEmpty bool
		// wantBlock is the exact [dash] block, nil when the notice has none.
		wantBlock []string
		// wantDash is what daemonconfig.Load must make of the block, checked
		// when checkDash is set.
		checkDash bool
		wantDash  daemonconfig.Dash
		wantIn    []string
		wantOut   []string
	}{
		{
			name:      "no file",
			missing:   true,
			data:      `{"search_roots":["/code"]}`,
			readErr:   errors.New("permission denied"),
			wantEmpty: true,
		},
		{
			name: "all three keys",
			data: `{"search_roots":["/code","/work"],"scan_interval":"30s","max_depth":5}`,
			wantBlock: []string{
				"[dash]",
				`search_roots = ["/code", "/work"]`,
				`scan_interval = "30s"`,
				"max_depth = 5",
			},
			checkDash: true,
			wantDash:  daemonconfig.Dash{SearchRoots: []string{"/code", "/work"}, ScanInterval: 30 * time.Second, MaxDepth: 5},
			wantIn: []string{
				"afk no longer reads " + noticeOldPath + ".",
				"search settings now live in " + noticeNewPath + ".",
				"Add these lines there.",
				"afk has not changed either file.",
				"delete the old file once the lines are moved",
			},
			wantOut: []string{"could not read", "nothing to move"},
		},
		{
			name:      "one key only",
			data:      `{"scan_interval":"2m"}`,
			wantBlock: []string{"[dash]", `scan_interval = "2m"`},
			checkDash: true,
			wantDash:  daemonconfig.Dash{ScanInterval: 2 * time.Minute, MaxDepth: daemonconfig.DefaultMaxDepth},
			wantOut:   []string{"search_roots", "max_depth"},
		},
		{
			name:      "roots as written, not expanded",
			data:      `{"search_roots":["~/code","rel/dir"]}`,
			wantBlock: []string{"[dash]", `search_roots = ["~/code", "rel/dir"]`},
		},
		{
			name: "an empty JSON object",
			data: `{}`,
			wantIn: []string{
				"afk no longer reads " + noticeOldPath + ".",
				"nothing to move",
				"afk has not changed either file.",
			},
			wantOut: []string{"Add these lines", "could not read"},
		},
		{
			name: "a null value counts as absent",
			data: `{"max_depth":null,"search_roots":null}`,
			wantIn: []string{
				"nothing to move",
			},
		},
		{
			name: "invalid JSON",
			data: `{not json`,
			wantBlock: []string{
				"[dash]",
				`# search_roots = ["~/code"]`,
				`# scan_interval = "60s"`,
				"# max_depth = 3",
			},
			checkDash: true,
			wantDash:  daemonconfig.Dash{ScanInterval: daemonconfig.DefaultScanInterval, MaxDepth: daemonconfig.DefaultMaxDepth},
			wantIn: []string{
				"afk no longer reads " + noticeOldPath + ".",
				"afk could not read the old file (it is not valid JSON)",
				"afk has not changed either file.",
			},
			// The parser's own message quotes a character of the file.
			wantOut: []string{"invalid character", "not json", "Add these lines there."},
		},
		{
			name: "JSON that is not an object",
			data: `null`,
			wantBlock: []string{
				"[dash]",
				`# search_roots = ["~/code"]`,
				`# scan_interval = "60s"`,
				"# max_depth = 3",
			},
			wantIn: []string{"afk could not read the old file (it is not valid JSON)"},
		},
		{
			name:    "a read error",
			readErr: errors.New("open " + noticeOldPath + ": permission denied"),
			wantBlock: []string{
				"[dash]",
				`# search_roots = ["~/code"]`,
				`# scan_interval = "60s"`,
				"# max_depth = 3",
			},
			wantIn: []string{
				"afk no longer reads " + noticeOldPath + ".",
				"afk could not read the old file (open " + noticeOldPath + ": permission denied)",
			},
		},
		{
			name: "an interval under the floor",
			data: `{"search_roots":["/code"],"scan_interval":"500ms"}`,
			wantBlock: []string{
				"[dash]",
				`search_roots = ["/code"]`,
				`# scan_interval is left out: "500ms" is under one second, the shortest afk accepts. Write "1s" or more.`,
			},
			checkDash: true,
			wantDash:  daemonconfig.Dash{SearchRoots: []string{"/code"}, ScanInterval: daemonconfig.DefaultScanInterval, MaxDepth: daemonconfig.DefaultMaxDepth},
		},
		{
			name:      "an interval exactly at the floor",
			data:      `{"scan_interval":"1s"}`,
			wantBlock: []string{"[dash]", `scan_interval = "1s"`},
			checkDash: true,
			wantDash:  daemonconfig.Dash{ScanInterval: time.Second, MaxDepth: daemonconfig.DefaultMaxDepth},
		},
		{
			name: "an interval that is not a duration",
			data: `{"scan_interval":"soon"}`,
			wantBlock: []string{
				"[dash]",
				`# scan_interval is left out: "soon" is not a length of time. Write it like "60s".`,
			},
			checkDash: true,
			wantDash:  daemonconfig.Dash{ScanInterval: daemonconfig.DefaultScanInterval, MaxDepth: daemonconfig.DefaultMaxDepth},
		},
		{
			name: "an interval of the wrong type",
			data: `{"scan_interval":60}`,
			wantBlock: []string{
				"[dash]",
				`# scan_interval is left out: the old file does not hold it as text in quotes. Write it like "60s".`,
			},
		},
		{
			name:      "an empty interval moves as written",
			data:      `{"scan_interval":""}`,
			wantBlock: []string{"[dash]", `scan_interval = ""`},
			checkDash: true,
			wantDash:  daemonconfig.Dash{ScanInterval: daemonconfig.DefaultScanInterval, MaxDepth: daemonconfig.DefaultMaxDepth},
		},
		{
			name: "a depth of 0",
			data: `{"max_depth":0,"scan_interval":"45s"}`,
			wantBlock: []string{
				"[dash]",
				`scan_interval = "45s"`,
				"# max_depth is left out: 0 is not a depth afk accepts. Write a whole number of 1 or more, such as 3.",
			},
			checkDash: true,
			wantDash:  daemonconfig.Dash{ScanInterval: 45 * time.Second, MaxDepth: daemonconfig.DefaultMaxDepth},
		},
		{
			name: "a negative depth",
			data: `{"max_depth":-2}`,
			wantBlock: []string{
				"[dash]",
				"# max_depth is left out: -2 is not a depth afk accepts. Write a whole number of 1 or more, such as 3.",
			},
		},
		{
			name: "a depth that is not a whole number",
			data: `{"max_depth":2.5}`,
			wantBlock: []string{
				"[dash]",
				"# max_depth is left out: the old file does not hold it as a whole number. Write it like 3, with no quotes.",
			},
		},
		{
			name: "roots of the wrong type",
			data: `{"search_roots":"/code","max_depth":2}`,
			wantBlock: []string{
				"[dash]",
				`# search_roots is left out: the old file does not hold it as a list of folders in quotes. Write it like ["~/code"].`,
				"max_depth = 2",
			},
			checkDash: true,
			wantDash:  daemonconfig.Dash{ScanInterval: daemonconfig.DefaultScanInterval, MaxDepth: 2},
		},
		{
			name: "a quote and a backslash in a root",
			data: `{"search_roots":["/co\"de/my \\ stuff","/tab\there","/bell\u0007"]}`,
			wantBlock: []string{
				"[dash]",
				`search_roots = ["/co\"de/my \\ stuff", "/tab\there", "/bell\u0007"]`,
			},
			checkDash: true,
			wantDash: daemonconfig.Dash{
				SearchRoots:  []string{`/co"de/my \ stuff`, "/tab\there", "/bell\a"},
				ScanInterval: daemonconfig.DefaultScanInterval,
				MaxDepth:     daemonconfig.DefaultMaxDepth,
			},
		},
		{
			name:      "an empty list of roots",
			data:      `{"search_roots":[]}`,
			wantBlock: []string{"[dash]", "search_roots = []"},
			checkDash: true,
			wantDash:  daemonconfig.Dash{ScanInterval: daemonconfig.DefaultScanInterval, MaxDepth: daemonconfig.DefaultMaxDepth},
		},
		{
			name:      "other keys are never echoed",
			data:      `{"token":"hunter2-secret","search_roots":["/code"],"nested":{"scan_interval":"9s","password":"swordfish"}}`,
			wantBlock: []string{"[dash]", `search_roots = ["/code"]`},
			wantOut:   []string{"hunter2", "token", "swordfish", "password", "nested", "9s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full, short := oldSettingsNotice(noticeOldPath, noticeNewPath, !tt.missing, []byte(tt.data), tt.readErr)

			if tt.wantEmpty {
				if full != "" || short != "" {
					t.Fatalf("full = %q, short = %q; want both empty when the old file does not exist", full, short)
				}
				return
			}
			if full == "" || short == "" {
				t.Fatalf("full = %q, short = %q; want a notice whenever the old file exists", full, short)
			}
			if !strings.HasSuffix(full, "\n") {
				t.Errorf("full notice does not end with a newline: %q", full)
			}

			if short != noticeShort {
				t.Errorf("short = %q\n want %q", short, noticeShort)
			}
			// The dashboard's toast is 48 columns by three lines and cuts
			// the rest off. 120 leaves room for its marker and for words
			// that wrap early.
			if len(short) > 120 {
				t.Errorf("short form is %d characters, too long for the toast to show whole: %q", len(short), short)
			}
			if strings.Contains(short, "\n") {
				t.Errorf("short form is not one line: %q", short)
			}

			block, _ := noticeBlock(full)
			if !reflect.DeepEqual(block, tt.wantBlock) {
				t.Errorf("[dash] block =\n  %s\nwant\n  %s", strings.Join(block, "\n  "), strings.Join(tt.wantBlock, "\n  "))
			}
			for _, want := range tt.wantIn {
				if !strings.Contains(full, want) {
					t.Errorf("full notice lacks %q:\n%s", want, full)
				}
			}
			for _, bad := range tt.wantOut {
				if strings.Contains(full, bad) || strings.Contains(short, bad) {
					t.Errorf("notice contains %q, which it must not:\n%s", bad, full)
				}
			}

			// Whatever was built must load cleanly. This runs for every row
			// with a block, not only the ones that state the result.
			if block != nil {
				cfg := loadNoticeBlock(t, block)
				if cfg.DashErr != nil {
					t.Errorf("the built lines are refused by the [dash] reader: %v\n%s", cfg.DashErr, strings.Join(block, "\n"))
				}
				if tt.checkDash && !reflect.DeepEqual(cfg.Dash, tt.wantDash) {
					t.Errorf("the built lines load as %+v, want %+v", cfg.Dash, tt.wantDash)
				}
			}
		})
	}
}

// fakeFileInfo is the least a stat answer needs for announceOldSettingsFile.
type fakeFileInfo struct {
	fs.FileInfo
	dir bool
}

func (f fakeFileInfo) IsDir() bool { return f.dir }

// noticeSource builds a source over a fake disk and records which paths were
// asked for, so a test can pin where the old file is looked for.
type noticeDisk struct {
	statErr  error
	dir      bool
	data     string
	readErr  error
	statted  []string
	readFrom []string
}

func (d *noticeDisk) source(configPath string) oldSettingsSource {
	return oldSettingsSource{
		configPath: configPath,
		stat: func(path string) (fs.FileInfo, error) {
			d.statted = append(d.statted, path)
			if d.statErr != nil {
				return nil, d.statErr
			}
			return fakeFileInfo{dir: d.dir}, nil
		},
		readFile: func(path string) ([]byte, error) {
			d.readFrom = append(d.readFrom, path)
			return []byte(d.data), d.readErr
		},
	}
}

// TestOldSettingsNoticeIsAnnouncedFromDisk covers the impure half with a fake disk.
//
// Load-bearing rows:
//   - "no old file" and "the folder cannot be looked into": nothing is
//     printed, nothing is returned and the file is not read. A stat that fails
//     for any reason is "no file", because the notice must never appear
//     without one.
//   - "the read fails after a good stat": the file is known to be there, so
//     the notice is still printed.
//   - every row pins that the only path ever looked at is config.json in the
//     folder of config.toml.
func TestOldSettingsNoticeIsAnnouncedFromDisk(t *testing.T) {
	const configPath = "/somewhere/else/afk/config.toml"
	const oldPath = "/somewhere/else/afk/config.json"

	tests := []struct {
		name       string
		configPath string
		disk       noticeDisk
		wantNotice bool
		wantRead   bool
		wantStat   []string
		wantIn     []string
	}{
		{
			name:       "an old file with settings",
			configPath: configPath,
			disk:       noticeDisk{data: `{"max_depth":4}`},
			wantNotice: true,
			wantRead:   true,
			wantStat:   []string{oldPath},
			wantIn:     []string{"afk no longer reads " + oldPath + ".", "now live in " + configPath + ".", "max_depth = 4"},
		},
		{
			name:       "no old file",
			configPath: configPath,
			disk:       noticeDisk{statErr: fs.ErrNotExist, data: `{"max_depth":4}`},
			wantStat:   []string{oldPath},
		},
		{
			name:       "the folder cannot be looked into",
			configPath: configPath,
			disk:       noticeDisk{statErr: fs.ErrPermission, data: `{"max_depth":4}`},
			wantStat:   []string{oldPath},
		},
		{
			name:       "a folder of that name",
			configPath: configPath,
			disk:       noticeDisk{dir: true},
			wantStat:   []string{oldPath},
		},
		{
			name:       "the read fails after a good stat",
			configPath: configPath,
			disk:       noticeDisk{readErr: errors.New("open " + oldPath + ": permission denied")},
			wantNotice: true,
			wantRead:   true,
			wantStat:   []string{oldPath},
			wantIn:     []string{"afk could not read the old file (open " + oldPath + ": permission denied)", "# max_depth = 3"},
		},
		{
			name:       "the config folder is unknown",
			configPath: "",
			disk:       noticeDisk{data: `{"max_depth":4}`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disk := tt.disk
			var out bytes.Buffer
			short := announceOldSettingsFile(&out, disk.source(tt.configPath))

			if !reflect.DeepEqual(disk.statted, tt.wantStat) {
				t.Errorf("looked at %v, want %v", disk.statted, tt.wantStat)
			}
			if tt.wantRead {
				if !reflect.DeepEqual(disk.readFrom, []string{oldPath}) {
					t.Errorf("read %v, want only %s", disk.readFrom, oldPath)
				}
			} else if len(disk.readFrom) != 0 {
				t.Errorf("read %v, want no read at all", disk.readFrom)
			}

			if !tt.wantNotice {
				if out.Len() != 0 || short != "" {
					t.Fatalf("printed %q and returned %q; want nothing", out.String(), short)
				}
				return
			}
			if short == "" {
				t.Fatal("no short form returned for the toast")
			}
			for _, want := range tt.wantIn {
				if !strings.Contains(out.String(), want) {
					t.Errorf("printed text lacks %q:\n%s", want, out.String())
				}
			}
		})
	}
}

// TestRunDashPrintsTheOldSettingsNoticeOncePerStart drives the start-up path
// with everything impure injected: the stderr writer, the disk and the
// program.
//
// What it pins, and why each matters:
//   - the full notice is on stderr BEFORE the program runs (checked from
//     inside the stub). Printed later it would be lost under the full-screen
//     dashboard or seen only after quitting.
//   - it is printed exactly once, and the disk is consulted exactly once.
//   - the short form reaches ui.Deps.StartupNotice through
//     dashDepsWithNotice, the function runDashProgram builds its deps with. A
//     notice that was decided and then dropped on the way to the Model fails
//     with no symptom at all.
//   - "no old file": nothing on stderr and an empty StartupNotice, so the
//     dashboard raises no toast.
func TestRunDashPrintsTheOldSettingsNoticeOncePerStart(t *testing.T) {
	const configPath = "/cfg/afk/config.toml"
	const firstLine = "afk no longer reads /cfg/afk/config.json."

	tests := []struct {
		name       string
		disk       noticeDisk
		wantNotice bool
	}{
		{name: "an old file", disk: noticeDisk{data: `{"search_roots":["/code"]}`}, wantNotice: true},
		{name: "an old file that is not JSON", disk: noticeDisk{data: `{not json`}, wantNotice: true},
		{name: "no old file", disk: noticeDisk{statErr: fs.ErrNotExist}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disk := tt.disk
			loc := dashLocation{gitCommonDir: t.TempDir()}
			var stderr bytes.Buffer

			var (
				runs           int
				printedAtStart string
				depsNotice     string
			)
			program := func(repo string, interval time.Duration, openSession string, got dashLocation, notice string) error {
				runs++
				printedAtStart = stderr.String()
				depsNotice = dashDepsWithNotice(openSession, got, nil, -1, notice).StartupNotice
				return nil
			}

			if err := runDashAnnouncing("/repo", 2*time.Second, "", loc, &stderr, disk.source(configPath), program); err != nil {
				t.Fatalf("runDashAnnouncing: %v", err)
			}
			if runs != 1 {
				t.Fatalf("the program ran %d times, want 1", runs)
			}
			if printedAtStart != stderr.String() {
				t.Errorf("stderr changed after the program started:\n at start: %q\n at end:   %q", printedAtStart, stderr.String())
			}

			if !tt.wantNotice {
				if stderr.Len() != 0 {
					t.Errorf("stderr = %q, want nothing without the old file", stderr.String())
				}
				if depsNotice != "" {
					t.Errorf("StartupNotice = %q, want empty without the old file", depsNotice)
				}
				return
			}

			if n := strings.Count(printedAtStart, firstLine); n != 1 {
				t.Errorf("the notice was printed %d times before the program started, want 1:\n%s", n, printedAtStart)
			}
			if len(disk.statted) != 1 || len(disk.readFrom) != 1 {
				t.Errorf("the disk was asked %d and read %d times, want once each", len(disk.statted), len(disk.readFrom))
			}
			if want := filepath.Join(filepath.Dir(configPath), "config.json"); disk.statted[0] != want {
				t.Errorf("looked at %s, want %s", disk.statted[0], want)
			}
			if depsNotice != noticeShort {
				t.Errorf("StartupNotice = %q\n want %q", depsNotice, noticeShort)
			}
		})
	}
}

// TestDashDepsCarryNoNoticeByDefault pins the other builder: dashDeps, which
// the tests and dashDepsWithNotice share, never invents a notice of its own.
func TestDashDepsCarryNoNoticeByDefault(t *testing.T) {
	if got := dashDeps("", dashLocation{}, nil, -1).StartupNotice; got != "" {
		t.Errorf("dashDeps(...).StartupNotice = %q, want empty", got)
	}
}
