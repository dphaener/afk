package activity

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
)

// Tail returns the non-empty lines in the last max bytes of the file at path.
// This is the package's one impure function: the logs only grow, so reading
// them whole would make the dashboard's cost climb for ever.
//
// When the file is bigger than max the read starts one byte early, at
// size-max-1, and everything up to and including the first newline is
// dropped. The extra byte is what tells a cut that lands exactly after a
// newline (the byte is that newline, so nothing real is dropped) from one in
// the middle of a line (the partial line is dropped). A torn last line is
// returned; the decoders skip it. A missing file is no log yet, which is
// normal, so it returns nil, nil.
func Tail(path string, max int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	cut := false
	if st.Size() > max {
		if _, err := f.Seek(st.Size()-max-1, io.SeekStart); err != nil {
			return nil, err
		}
		cut = true
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	s := string(b)
	if cut {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return nil, nil
		}
		s = s[i+1:]
	}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}
