package daemon

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Binary is the identity of afk's own program file: where it is, how big it
// is and when it was last written. The zero value means "not known".
//
// It is size and modification time and not a hash on purpose. The check runs
// at the top of every pass, and reading a large program file that often to
// catch a change that a rebuild or a reinstall always makes visible in one of
// the two is not worth the work.
type Binary struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// ReadBinary stats path, following symlinks, so a link that now points at a
// new file counts as a change. An empty path, a missing file and a folder are
// errors: none of them is a program file to compare.
func ReadBinary(path string) (Binary, error) {
	if path == "" {
		return Binary{}, errors.New("no path to afk's program file")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return Binary{}, err
	}
	if fi.IsDir() {
		return Binary{}, fmt.Errorf("%s is a folder, not a program file", path)
	}
	return Binary{Path: path, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// NewBinary reports whether the program file on disk is no longer the one
// this daemon started from. It is true only when the start identity is known,
// the file was read now, and the size or modification time differ.
//
// Every doubt answers false. A daemon that cannot tell must carry on: a
// service manager restarting it on a guess would cut a build loop short for
// nothing, and an unreadable file is as likely a passing glitch as a real
// replacement (a reinstall that is half done is read again on the next pass).
func NewBinary(start, now Binary, readErr error) bool {
	if readErr != nil || start == (Binary{}) {
		return false
	}
	return start.Size != now.Size || !start.ModTime.Equal(now.ModTime)
}
