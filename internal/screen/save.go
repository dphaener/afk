package screen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dphaener/afk/internal/rex"
)

// FileName is the page Save writes inside a session folder.
const FileName = "terminal.html"

// saveLimit is how much of the end of the output Save keeps. A long
// scrollback would make a page too big for a phone to open.
const saveLimit = 2 << 20

// BlockRef names one Rex block. It is declared here, not taken from
// internal/host, so this package depends on neither the host nor its file
// format; the caller copies the ids across.
type BlockRef struct {
	SessionID string
	WindowID  string
	BlockID   string
}

// ErrBlockGone is what Save returns (wrapped) when Rex says the session or
// block does not exist, so a caller can tell "the screen is gone" from a
// failed read with errors.Is.
var ErrBlockGone = errors.New("the Rex block is gone, so its screen cannot be saved")

// Save reads a Rex block's screen once as html and writes it as a standalone
// page at {dir}/terminal.html.
//
// It writes a temp file in dir and renames it into place because the web page
// reads the file while a later save may run: a rename means a reader sees
// the old page or the new one, never half of one. A failed read or write
// leaves any earlier terminal.html untouched.
func Save(ctx context.Context, c rex.Caller, block BlockRef, dir string) error {
	data, err := rex.Format(ctx, c, block.SessionID, block.BlockID, "html")
	if err != nil {
		var ce *rex.CallError
		if errors.As(err, &ce) && ce.ExitCode() == 3 {
			return fmt.Errorf("%w: %w", ErrBlockGone, err)
		}
		return fmt.Errorf("read the screen of Rex block %s: %w", block.BlockID, err)
	}
	page := Document(Tail(data, saveLimit))

	tmp, err := os.CreateTemp(dir, FileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("save the screen in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(page)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmpName, filepath.Join(dir, FileName))
	}
	if werr != nil {
		os.Remove(tmpName)
		return fmt.Errorf("save the screen in %s: %w", dir, werr)
	}
	return nil
}
