package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// seamForbidden lists, by import-path suffix, the calls that drive a session's
// host directly. Every one of them has a runtime-seam twin on
// session.Runtime, reached through session.RuntimeFor.
var seamForbidden = map[string]map[string]bool{
	"internal/session": {"SpawnHost": true, "StopHost": true},
	"internal/host": {
		"SendInput": true, "SendKeys": true, "RequestRestart": true,
		"RequestExit": true, "Dial": true, "Attach": true,
	},
}

// seamViolations parses one source file and returns "file:line: pkg.Func"
// for each selector call that bypasses the runtime seam. It resolves the
// local name from the file's imports, so an aliased import is still caught
// and an unrelated identifier that merely happens to be called "host" is not.
// Comments and strings are never calls, so they are not flagged.
func seamViolations(t *testing.T, filename string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	// local import name -> forbidden function set
	locals := map[string]map[string]bool{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		for suffix, funcs := range seamForbidden {
			if p != suffix && !strings.HasSuffix(p, "/"+suffix) {
				continue
			}
			name := path.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name == "_" || name == "." {
				continue
			}
			locals[name] = funcs
		}
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if funcs, ok := locals[id.Name]; ok && funcs[sel.Sel.Name] {
			pos := fset.Position(call.Pos())
			out = append(out, pos.Filename+":"+strconv.Itoa(pos.Line)+": "+id.Name+"."+sel.Sel.Name)
		}
		return true
	})
	return out
}

// TestSeamMatcher table-tests the matcher on small snippets. Load-bearing
// rows: "direct call" (the basic catch), "aliased import" (a rename must not
// hide a bypass), "runtime method allowed" (rt.Spawn is the seam itself) and
// "comment mention allowed" (docs name these functions constantly).
func TestSeamMatcher(t *testing.T) {
	const hdr = "package x\n"
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"direct call", hdr + "import \"github.com/dphaener/afk/internal/host\"\nfunc f() { host.SendInput(\"a\", \"b\", \"c\", 0) }\n", 1},
		{"aliased import", hdr + "import h \"github.com/dphaener/afk/internal/host\"\nfunc f() { _ = h.RequestExit(\"a\", \"b\", 0) }\n", 1},
		{"session spawn", hdr + "import \"github.com/dphaener/afk/internal/session\"\nfunc f() { session.SpawnHost(session.HostSpawn{}, session.Deps{}) }\n", 1},
		{"runtime method allowed", hdr + "import \"github.com/dphaener/afk/internal/session\"\nfunc f(rt session.Runtime) { rt.Spawn(nil) }\n", 0},
		{"comment mention allowed", hdr + "import \"github.com/dphaener/afk/internal/host\"\n// host.SendInput is wrapped elsewhere.\nfunc f() { _ = host.ReadMeta }\n", 0},
		{"unrelated local named host", hdr + "type h struct{}\nfunc (h) SendInput() {}\nfunc f() { var host h; host.SendInput() }\n", 0},
		{"other host function allowed", hdr + "import \"github.com/dphaener/afk/internal/host\"\nfunc f() { host.ReadMeta(\"a\") }\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := seamViolations(t, "snippet.go", tc.src)
			if len(got) != tc.want {
				t.Errorf("got %v, want %d violation(s)", got, tc.want)
			}
		})
	}
}

// TestSeamCallersUseRuntime fails for any production file that drives a host
// directly instead of through session.RuntimeFor. T17.7 adds a second runtime;
// a caller that skips the seam would silently drive a Rex session as an afk
// one. internal/host and internal/session own the implementations, and
// plugins, vendor and testdata hold no module code.
func TestSeamCallersUseRuntime(t *testing.T) {
	skipDir := map[string]bool{
		"internal/host": true, "internal/session": true,
		"vendor": true, "plugins": true,
	}
	var bad []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(p)
		if d.IsDir() {
			base := d.Name()
			if rel != "." && (strings.HasPrefix(base, ".") || base == "testdata" || skipDir[rel]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		bad = append(bad, seamViolations(t, rel, nil)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s bypasses the runtime seam; go through session.RuntimeFor", b)
	}
}
