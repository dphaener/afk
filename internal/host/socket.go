package host

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

const (
	maxNamePrefixLen = 20
	maxSocketPathLen = 100
)

// SocketPath derives the Unix domain socket path for the host process of
// session name in repoRoot: $TMPDIR/afk-<uid>/<name>-<8-char-hash>.sock.
// The name prefix is truncated to maxNamePrefixLen so a long session name
// (up to 64 chars) still fits comfortably within the ~104-byte sun_path
// limit most platforms impose on Unix sockets; the hash (of repoRoot+name)
// disambiguates the same session name used in different repos. SocketPath
// creates the parent directory (0700) as a side effect. It only errors on
// path length as a last resort — e.g. a pathological TMPDIR — never as the
// first response to a long name.
func SocketPath(repoRoot, name string) (string, error) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("afk-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating socket dir %s: %w", dir, err)
	}

	prefix := name
	if len(prefix) > maxNamePrefixLen {
		prefix = prefix[:maxNamePrefixLen]
	}
	sum := sha256.Sum256([]byte(repoRoot + name))
	hash := fmt.Sprintf("%x", sum)[:8]

	path := filepath.Join(dir, fmt.Sprintf("%s-%s.sock", prefix, hash))
	if len(path) > maxSocketPathLen {
		return "", fmt.Errorf("socket path %q is %d bytes, exceeds %d-byte limit even after truncating the name prefix to %d chars (TMPDIR too long?)", path, len(path), maxSocketPathLen, maxNamePrefixLen)
	}
	return path, nil
}
