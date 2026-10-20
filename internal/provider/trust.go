package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrTrustUnsupported means the provider has no folder-trust record afk
// knows how to write. Callers ignore it: the owner simply answers the agent
// CLI's own question the first time, as before.
var ErrTrustUnsupported = errors.New("afk cannot pre-trust folders for that agent CLI, so the owner answers its safety question")

// TrustDeps is everything TrustFolder touches on the machine, so tests write
// only under a temporary folder and never read the real environment.
type TrustDeps struct {
	// Home is the user's home directory.
	Home string
	// Getenv reads an environment variable, normally os.Getenv.
	Getenv func(string) string
	// ReadFile reads a file, normally os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// Stat reports a file's mode, normally os.Stat.
	Stat func(string) (fs.FileInfo, error)
	// WriteTemp writes data to a new file in dir with the given mode and
	// returns its path. It must create the file in dir so the rename stays
	// on one filesystem.
	WriteTemp func(dir string, data []byte, mode fs.FileMode) (string, error)
	// Rename moves a file over another, normally os.Rename.
	Rename func(from, to string) error
	// Remove deletes a file, normally os.Remove; cleans up the temporary
	// file when the rename fails.
	Remove func(string) error
	// EvalSymlinks resolves links in a path, normally filepath.EvalSymlinks.
	EvalSymlinks func(string) (string, error)
}

// DefaultTrustDeps returns the real-machine deps for home.
func DefaultTrustDeps(home string) TrustDeps {
	w := DefaultUnattendedWriteDeps(home)
	return TrustDeps{
		Home:         home,
		Getenv:       os.Getenv,
		ReadFile:     w.ReadFile,
		Stat:         w.Stat,
		WriteTemp:    w.WriteTemp,
		Rename:       w.Rename,
		Remove:       w.Remove,
		EvalSymlinks: filepath.EvalSymlinks,
	}
}

// claudeStatePath is where the agent CLI keeps its per-folder state: the
// file under its config folder when CLAUDE_CONFIG_DIR is set, else the one
// in the home folder.
func claudeStatePath(getenv func(string) string, home string) string {
	if getenv != nil {
		if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			return filepath.Join(dir, ".claude.json")
		}
	}
	return filepath.Join(home, ".claude.json")
}

// TrustFolder records that the owner trusts folder, so the agent CLI opens a
// planning session there straight at its input prompt instead of stopping at
// its "do you trust this folder" screen. That screen halts a planning
// session before any conversation exists, and the owner would have to leave
// the dashboard or web page to answer it (T14.1, inbox item vbkr6s). afk
// calls this only for a folder it has just created itself.
//
// Only projects[folder].hasTrustDialogAccepted is set to true. Every other
// key, in this entry and elsewhere, is carried through untouched: the file
// also holds the owner's history and account state. Values are re-encoded
// with HTML escaping off, because Go's default would rewrite <, > and &
// inside strings the owner wrote, and numbers are kept as written.
//
// The key is the folder's absolute path with symbolic links resolved, since
// the CLI keys by its working directory, which the kernel reports with links
// resolved. A missing file, or one that is not a JSON object, is never
// created or overwritten: a broken file may be the owner's only copy. The
// write goes through a temporary file in the same folder and a rename. The
// file is read immediately before the write, but the CLI rewrites it too, so
// an update it makes in that instant can be lost; that window is accepted.
// Errors never quote the file's content.
func TrustFolder(name, folder string, d TrustDeps) error {
	p, err := Lookup(name)
	if err != nil {
		return err
	}
	if _, ok := p.(claude); !ok {
		return ErrTrustUnsupported
	}
	if d.ReadFile == nil || d.WriteTemp == nil || d.Rename == nil {
		return errors.New("afk could not update the agent CLI's folder trust on this machine")
	}
	const notObject = "the agent CLI's state file is not what afk expects, so afk left it alone"

	key, err := filepath.Abs(folder)
	if err != nil {
		return errors.New("afk could not work out the full path of the new folder")
	}
	if d.EvalSymlinks != nil {
		if resolved, err := d.EvalSymlinks(key); err == nil {
			key = resolved
		}
	}

	path := claudeStatePath(d.Getenv, d.Home)
	data, err := d.ReadFile(path)
	if err != nil {
		return errors.New("afk could not read the agent CLI's state file, so it did not pre-trust the new folder")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil || top == nil || dec.More() {
		return errors.New(notObject)
	}

	projects := map[string]json.RawMessage{}
	if raw, ok := top["projects"]; ok {
		if err := json.Unmarshal(raw, &projects); err != nil || projects == nil {
			return errors.New(notObject)
		}
	}
	entry := map[string]json.RawMessage{}
	if raw, ok := projects[key]; ok {
		if err := json.Unmarshal(raw, &entry); err != nil || entry == nil {
			return errors.New(notObject)
		}
	}
	entry["hasTrustDialogAccepted"] = json.RawMessage("true")

	if projects[key], err = encodeNoEscape(entry); err != nil {
		return errors.New("afk could not encode the agent CLI's state file")
	}
	if top["projects"], err = encodeNoEscape(projects); err != nil {
		return errors.New("afk could not encode the agent CLI's state file")
	}
	out, err := encodeNoEscape(top)
	if err != nil {
		return errors.New("afk could not encode the agent CLI's state file")
	}

	mode := fs.FileMode(0o600)
	if d.Stat != nil {
		if fi, err := d.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
	}
	tmp, err := d.WriteTemp(filepath.Dir(path), append(out, '\n'), mode)
	if err != nil {
		return errors.New("afk could not write the agent CLI's state file. Check that its folder is writable")
	}
	if err := d.Rename(tmp, path); err != nil {
		if d.Remove != nil {
			d.Remove(tmp)
		}
		return errors.New("afk could not replace the agent CLI's state file. Check that its folder is writable")
	}
	return nil
}

// encodeNoEscape marshals v with two-space indent and HTML escaping off.
func encodeNoEscape(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}
