package cliagents

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// spySource wraps the real Markdown source and counts Scan calls per scope,
// so freshness tests observe rescans without parsing files.
type spySource struct {
	inner memory.MarkdownSource
	mu    sync.Mutex
	scans map[memory.Scope]int
}

func newSpySource(inner memory.MarkdownSource) *spySource {
	return &spySource{inner: inner, scans: make(map[memory.Scope]int)}
}

func (s *spySource) Scan(ctx context.Context, scope memory.Scope) ([]memory.MarkdownDocument, error) {
	s.mu.Lock()
	s.scans[scope]++
	s.mu.Unlock()
	return s.inner.Scan(ctx, scope)
}

func (s *spySource) Save(ctx context.Context, e memory.Entry) (memory.MarkdownDocument, error) {
	return s.inner.Save(ctx, e)
}

func (s *spySource) Delete(ctx context.Context, path string) error {
	return s.inner.Delete(ctx, path)
}

func (s *spySource) ProjectDir() string { return s.inner.ProjectDir() }
func (s *spySource) OrgDir() string     { return s.inner.OrgDir() }

func (s *spySource) scanCount(scope memory.Scope) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scans[scope]
}

// newFreshnessStore opens a Markdown store over a spy source with a real
// SQLite index. The initial open refresh is already counted; tests measure
// deltas from their baseline.
func newFreshnessStore(t *testing.T) (*markdownStore, *spySource, memory.MarkdownSource) {
	t.Helper()
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	source, err := memory.NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	spy := newSpySource(source)
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: spy, Index: index, ProjectID: root})
	if err != nil {
		t.Fatal(err)
	}
	return store.(*markdownStore), spy, source
}

// attachFresh simulates a healthy reconciler that just synced the project
// scope. Degraded and lastSync maps are live store state, so tests set them
// directly.
func attachFresh(s *markdownStore) {
	s.reconcilerAttached = true
	s.fallback = 30 * time.Second
	s.lastSync = map[memory.Scope]time.Time{memory.ScopeProject: time.Now()}
	s.degraded = make(map[memory.Scope]bool)
}

func TestMarkdownStoreReadSkipsRescanWhileScopeFresh(t *testing.T) {
	ctx := context.Background()
	t.Run("search", func(t *testing.T) {
		s, spy, _ := newFreshnessStore(t)
		attachFresh(s)
		before := spy.scanCount(memory.ScopeProject)
		if _, err := s.Search(ctx, memory.Query{Text: "anything", Scope: memory.ScopeProject}); err != nil {
			t.Fatal(err)
		}
		if got := spy.scanCount(memory.ScopeProject); got != before {
			t.Fatalf("project scans %d -> %d, want no rescan while fresh", before, got)
		}
	})
	t.Run("count", func(t *testing.T) {
		s, spy, _ := newFreshnessStore(t)
		attachFresh(s)
		before := spy.scanCount(memory.ScopeProject)
		if _, err := s.Count(ctx, memory.ScopeProject); err != nil {
			t.Fatal(err)
		}
		if got := spy.scanCount(memory.ScopeProject); got != before {
			t.Fatalf("project scans %d -> %d, want no rescan while fresh", before, got)
		}
	})
	t.Run("core entries", func(t *testing.T) {
		s, spy, _ := newFreshnessStore(t)
		attachFresh(s)
		before := spy.scanCount(memory.ScopeProject)
		if _, err := s.CoreEntries(ctx, memory.ScopeProject); err != nil {
			t.Fatal(err)
		}
		if got := spy.scanCount(memory.ScopeProject); got != before {
			t.Fatalf("project scans %d -> %d, want no rescan while fresh", before, got)
		}
	})
	t.Run("promote to core", func(t *testing.T) {
		s, spy, _ := newFreshnessStore(t)
		attachFresh(s)
		result, err := s.Save(ctx, memory.Entry{Title: "Fresh promote", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "Saved before the read.", Why: "Promotion needs an entry."})
		if err != nil {
			t.Fatal(err)
		}
		before := spy.scanCount(memory.ScopeProject)
		if err := s.PromoteToCore(ctx, result.ID); err != nil {
			t.Fatal(err)
		}
		if got := spy.scanCount(memory.ScopeProject); got != before {
			t.Fatalf("project scans %d -> %d, want no rescan while fresh", before, got)
		}
	})
}

func TestMarkdownStoreReadRescansWhenScopeDegraded(t *testing.T) {
	s, spy, _ := newFreshnessStore(t)
	attachFresh(s)
	s.degraded[memory.ScopeProject] = true
	before := spy.scanCount(memory.ScopeProject)
	if _, err := s.Search(context.Background(), memory.Query{Text: "anything", Scope: memory.ScopeProject}); err != nil {
		t.Fatal(err)
	}
	if got := spy.scanCount(memory.ScopeProject); got <= before {
		t.Fatalf("project scans %d -> %d, want a rescan while degraded", before, got)
	}
}

func TestMarkdownStoreReadRescansWhenFreshnessExpired(t *testing.T) {
	s, spy, _ := newFreshnessStore(t)
	attachFresh(s)
	s.lastSync[memory.ScopeProject] = time.Now().Add(-time.Minute)
	before := spy.scanCount(memory.ScopeProject)
	if _, err := s.Search(context.Background(), memory.Query{Text: "anything", Scope: memory.ScopeProject}); err != nil {
		t.Fatal(err)
	}
	if got := spy.scanCount(memory.ScopeProject); got <= before {
		t.Fatalf("project scans %d -> %d, want a rescan after the TTL expired", before, got)
	}
}

func TestMarkdownStoreReadRescansWithoutReconciler(t *testing.T) {
	s, spy, _ := newFreshnessStore(t)
	s.fallback = 30 * time.Second
	s.lastSync = map[memory.Scope]time.Time{memory.ScopeProject: time.Now()}
	before := spy.scanCount(memory.ScopeProject)
	if _, err := s.Search(context.Background(), memory.Query{Text: "anything", Scope: memory.ScopeProject}); err != nil {
		t.Fatal(err)
	}
	if got := spy.scanCount(memory.ScopeProject); got <= before {
		t.Fatalf("project scans %d -> %d, want a rescan with no reconciler attached", before, got)
	}
}

func TestMarkdownStoreSaveRescansAndStampsFreshness(t *testing.T) {
	s, spy, _ := newFreshnessStore(t)
	attachFresh(s)
	stale := time.Now().Add(-time.Minute)
	s.lastSync[memory.ScopeProject] = stale
	if _, err := s.Save(context.Background(), memory.Entry{Title: "Stamp on save", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "Save always syncs.", Why: "Mutations keep the index current."}); err != nil {
		t.Fatal(err)
	}
	if got := spy.scanCount(memory.ScopeProject); got < 1 {
		t.Fatalf("project scans after save=%d, want at least one", got)
	}
	s.mu.Lock()
	stamped := s.lastSync[memory.ScopeProject]
	s.mu.Unlock()
	if !stamped.After(stale) {
		t.Fatalf("lastSync after save=%v, want stamped after %v", stamped, stale)
	}
}

// TestSyncScopeAndDeleteNeverResurrectEntry guards the resurrected-entry
// interleaving: syncScope and Delete serialize on s.mu, so once Delete
// returns, no in-flight or later sync can carry a pre-delete scan into the
// index. Run under -race via the package gate.
func TestSyncScopeAndDeleteNeverResurrectEntry(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newFreshnessStore(t)
	result, err := s.Save(ctx, memory.Entry{Title: "Doomed fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "resurrection marker", Why: "Deleted while syncs spin."})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = s.syncScope(ctx, memory.ScopeProject)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	if err := s.Delete(ctx, result.ID); err != nil {
		close(stop)
		wg.Wait()
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	hits, err := s.Search(ctx, memory.Query{Text: "resurrection marker", Scope: memory.ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("deleted entry resurrected: %+v", hits)
	}
	if _, err := s.cfg.Index.FindMemoryIndexEntry(ctx, result.ID, s.cfg.ProjectID, s.cfg.OrgID); err == nil {
		t.Fatal("deleted id still present in the index")
	}
}
