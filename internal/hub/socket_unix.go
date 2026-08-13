//go:build !windows

package hub

import (
	"net"
	"os"
	"path/filepath"
)

// listen binds this workspace's hub Unix socket. A stale socket file from a
// crashed prior owner is removed first - safe because the caller only
// reaches here after winning the election lock, so no live owner can be
// bound to it.
func listen(storeDir string) (net.Listener, error) {
	path := filepath.Join(storeDir, "hub.sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

// dial connects to an already-elected owner's Unix socket.
func dial(storeDir string) (net.Conn, error) {
	path := filepath.Join(storeDir, "hub.sock")
	return net.DialTimeout("unix", path, dialSocketTimeout)
}
