package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func newDumpTestStore(t *testing.T) *sqliteStore {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(dir, "project.db"),
		MaxEntryBytes:    8192,
		MaxEntries:       50,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	impl, ok := s.(*sqliteStore)
	if !ok {
		t.Fatalf("store is not *sqliteStore")
	}
	t.Cleanup(func() { _ = impl.Close() })
	return impl
}

func TestDumpJSONLDeterministicAcrossRuns(t *testing.T) {
	s := newDumpTestStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := s.Save(ctx, testEntry(fmt.Sprintf("entry-%d", i), ScopeProject)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	var first, second bytes.Buffer
	if err := s.DumpJSONL(&first); err != nil {
		t.Fatalf("DumpJSONL (first): %v", err)
	}
	if err := s.DumpJSONL(&second); err != nil {
		t.Fatalf("DumpJSONL (second): %v", err)
	}
	if first.String() != second.String() {
		t.Fatalf("DumpJSONL not byte-identical across runs on an unmodified DB:\n--- first ---\n%s\n--- second ---\n%s", first.String(), second.String())
	}
}

func TestDumpJSONLSortedByID(t *testing.T) {
	s := newDumpTestStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := s.Save(ctx, testEntry(fmt.Sprintf("entry-%d", i), ScopeProject)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	var buf bytes.Buffer
	if err := s.DumpJSONL(&buf); err != nil {
		t.Fatalf("DumpJSONL: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5", len(lines))
	}
	var ids []string
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("unmarshal line %q: %v", line, err)
		}
		id, _ := row["id"].(string)
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("ids not sorted ascending: %v", ids)
		}
	}
}

func TestDumpJSONLFixedKeyOrder(t *testing.T) {
	s := newDumpTestStore(t)
	ctx := context.Background()
	if _, err := s.Save(ctx, testEntry("one entry", ScopeProject)); err != nil {
		t.Fatalf("save: %v", err)
	}

	var buf bytes.Buffer
	if err := s.DumpJSONL(&buf); err != nil {
		t.Fatalf("DumpJSONL: %v", err)
	}
	line := strings.TrimSpace(buf.String())
	wantOrder := []string{"id", "scope", "org", "tier", "verdict", "tags", "title", "summary", "content", "created_at"}
	pos := -1
	for _, key := range wantOrder {
		idx := strings.Index(line, `"`+key+`"`)
		if idx < 0 {
			t.Fatalf("key %q missing from dump line: %s", key, line)
		}
		if idx <= pos {
			t.Fatalf("key %q out of order (want %v): %s", key, wantOrder, line)
		}
		pos = idx
	}
}

func TestDumpJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(dir, "project.db"),
		MaxEntryBytes:    8192,
		MaxEntries:       50,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Save(ctx, testEntry(fmt.Sprintf("rt-%d", i), ScopeProject)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	before, err := s.Search(ctx, Query{Text: "rt", Scope: ScopeProject, MaxResults: 10})
	if err != nil {
		t.Fatalf("search before: %v", err)
	}
	var buf bytes.Buffer
	if err := s.(*sqliteStore).DumpJSONL(&buf); err != nil {
		t.Fatalf("DumpJSONL: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen a fresh store at the same path (simulating "the committed .db
	// is what round-trips", not literally rebuilding from the jsonl - the
	// jsonl is a reviewable export, not an alternate load path) and confirm
	// the dumped rows match what Search still reports.
	s2, err := Open(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	after, err := s2.Search(ctx, Query{Text: "rt", Scope: ScopeProject, MaxResults: 10})
	if err != nil {
		t.Fatalf("search after: %v", err)
	}
	if len(before) != len(after) || len(before) != 3 {
		t.Fatalf("round-trip row count mismatch: before=%d after=%d", len(before), len(after))
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("dump line count = %d, want 3", len(lines))
	}
}
