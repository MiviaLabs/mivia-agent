package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// Uses a streaming json.Decoder so lines are unbounded, matching writeJSONL,
// which encodes whole messages with no size ceiling. A bufio.Scanner-based
// reader capped each JSONL line at 4 MiB, so a single tool result over that
// bound made every load path fail with bufio.ErrTooLong and the session was
// permanently unloadable. Blank lines and trailing newlines are skipped by the
// decoder exactly as TrimSpace skipped them before; empty files yield no
// messages; malformed or truncated JSON is reported as an error, never
// silently truncated.
func readJSONL(path string) ([]provider.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var msgs []provider.Message
	dec := json.NewDecoder(f)
	for {
		var m provider.Message
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return msgs, fmt.Errorf("parse line: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
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
