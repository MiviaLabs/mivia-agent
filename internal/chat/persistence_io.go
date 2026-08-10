package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// syncFile is the sync function used by writeMetaJSON. Exposed as a package-level
// variable so tests can replace it with a tracking wrapper to verify Sync is called.
var syncFile = (*os.File).Sync

// --- File I/O ---

// writeJSONL writes messages as JSONL (one JSON object per line).
// Uses a buffered encoder for performance on large sessions.
func writeJSONL(path string, msgs []provider.Message) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	// Sync to ensure durability.
	return f.Sync()
}

// writeMetaJSON writes the metadata JSON atomically (temp file + rename).
// Uses a unique temp file per call via os.CreateTemp to prevent races
// when multiple goroutines save to the same session directory.
func writeMetaJSON(dir string, meta sessionMeta) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("metadata directory is required")
	}
	f, err := os.CreateTemp(dir, ".meta-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := f.Name()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	// Sync to ensure durability before rename (matches writeJSONL contract).
	if err := syncFile(f); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	f.Close()

	metaPath := filepath.Join(dir, metaFileName)
	if err := os.Rename(tmpPath, metaPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// readJSONL reads messages from a JSONL file.
// Uses a scanner with a large buffer for tool result lines.
func readJSONL(path string) ([]provider.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var msgs []provider.Message
	sc := bufio.NewScanner(f)
	// 256KB initial buffer, growable to 4MB for large tool results.
	buf := make([]byte, 0, 256*1024)
	sc.Buffer(buf, 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m provider.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return msgs, fmt.Errorf("parse line: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, sc.Err()
}

// readMetaJSON reads the metadata file for a session directory.
func readMetaJSON(dir string) (*sessionMeta, error) {
	metaPath := filepath.Join(dir, metaFileName)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read meta: %w", err)
	}
	var meta sessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	return &meta, nil
}
