//go:build windows

package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"path/filepath"

	"github.com/Microsoft/go-winio"
)

// pipeName derives a stable named-pipe path from storeDir. Windows named
// pipes are a flat global namespace (unlike a Unix socket, which is just a
// file at storeDir itself), so identity has to come from a hash of the
// directory rather than the directory acting as the address.
func pipeName(storeDir string) string {
	resolved, err := filepath.Abs(storeDir)
	if err != nil {
		resolved = filepath.Clean(storeDir)
	}
	digest := sha256.Sum256([]byte(resolved))
	return `\\.\pipe\mivia-hub-` + hex.EncodeToString(digest[:8])
}

func listen(storeDir string) (net.Listener, error) {
	return winio.ListenPipe(pipeName(storeDir), nil)
}

func dial(storeDir string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialSocketTimeout)
	defer cancel()
	return winio.DialPipeContext(ctx, pipeName(storeDir))
}
