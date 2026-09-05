package cliagents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestMarkdownSourceScanIgnoresReadme(t *testing.T) {
	root := t.TempDir()
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Documentation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if docs, err := source.Scan(context.Background(), memory.ScopeProject); err != nil || len(docs) != 0 {
		t.Fatalf("Scan docs=%d err=%v, want no documents", len(docs), err)
	}
}

func TestMarkdownStoreSaveSearchCountAndReopen(t *testing.T) {
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	e := memory.Entry{Title: "Cache lock", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "Use a lock.", Why: "Writers overlap."}
	first, err := store.Save(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Save(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("IDs differ: %q %q", first.ID, second.ID)
	}
	hits, err := store.Search(context.Background(), memory.Query{Text: "cache lock", Scope: memory.ScopeProject})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%d err=%v", len(hits), err)
	}
	if count, err := store.Count(context.Background(), memory.ScopeProject); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if count, err := reopened.Count(context.Background(), memory.ScopeProject); err != nil || count != 1 {
		t.Fatalf("reopened count=%d err=%v", count, err)
	}
}

func TestMarkdownStoreReadOnlyRejectsSave(t *testing.T) {
	source, err := memory.NewMarkdownSource(t.TempDir(), filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Save(context.Background(), memory.Entry{Title: "no", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "no", Why: "no"}); err == nil {
		t.Fatal("read-only save succeeded")
	}
}

func TestOpenMemoryStoreUsesMarkdownAndGlobalIndex(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenMemoryStoreWithReadOnly(root, config.MemoryConfig{
		StoreBackend: memory.BackendMarkdown, MaxEntryBytes: memory.DefaultMaxEntryBytes,
		MaxSearchResults: memory.DefaultMaxSearchResults,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Save(context.Background(), memory.Entry{
		Title: "Global index", Scope: memory.ScopeProject, Verdict: memory.VerdictGood,
		Summary: "Use Markdown files.", Why: "The index is derived.",
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := store.Search(context.Background(), memory.Query{Text: "Markdown", Scope: memory.ScopeProject})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%d err=%v, want one Markdown result", len(hits), err)
	}
	if _, err := os.Stat(workspace.GlobalContextStorePath(root)); err != nil {
		t.Fatalf("global context index: %v", err)
	}
}

func TestReadOnlySearchRefreshesChangedMarkdownIndex(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	indexPath := filepath.Join(home, ".mivia", "context.db")
	source, err := memory.NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Save(context.Background(), memory.Entry{Title: "Old fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "old marker", Why: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Save(context.Background(), memory.Entry{Title: "New fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "new marker", Why: "test"}); err != nil {
		t.Fatal(err)
	}
	oldDoc, err := source.Scan(context.Background(), memory.ScopeProject)
	if err != nil || len(oldDoc) != 2 {
		t.Fatalf("scan after add: %d, %v", len(oldDoc), err)
	}
	var oldPath string
	for _, doc := range oldDoc {
		if doc.Entry.Title == "Old fact" {
			oldPath = doc.Path
		}
	}
	if oldPath == "" {
		t.Fatal("old memory path not found")
	}
	if err := source.Delete(context.Background(), oldPath); err != nil {
		t.Fatal(err)
	}
	store, err = OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: root, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hits, err := store.Search(context.Background(), memory.Query{Text: "new marker", Scope: memory.ScopeProject})
	if err != nil || len(hits) != 1 || hits[0].Title != "New fact" {
		t.Fatalf("new search: %+v, %v", hits, err)
	}
	hits, err = store.Search(context.Background(), memory.Query{Text: "old marker", Scope: memory.ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("deleted memory remained indexed: %+v", hits)
	}
}

func TestMarkdownStorePromoteCoreAndDelete(t *testing.T) {
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.Entry{Title: "Tiered memory", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "Keep this fact.", Why: "It is useful."}
	result, err := store.Save(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteToCore(context.Background(), result.ID); err != nil {
		t.Fatal(err)
	}
	core, err := store.CoreEntries(context.Background(), memory.ScopeProject)
	if err != nil || len(core) != 1 || core[0].ID != result.ID {
		t.Fatalf("core=%+v err=%v, want promoted entry", core, err)
	}
	if err := store.Delete(context.Background(), result.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := store.Count(context.Background(), memory.ScopeProject); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v, want zero after delete", count, err)
	}
}

func TestMarkdownStoreSerializesConcurrentSaves(t *testing.T) {
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = store.Save(context.Background(), memory.Entry{Title: fmt.Sprintf("Concurrent %d", i), Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "A concurrent fact.", Why: "The store serializes writes."})
		}(i)
	}
	wg.Wait()
	if count, err := store.Count(context.Background(), memory.ScopeProject); err != nil || count != 8 {
		t.Fatalf("count=%d err=%v, want eight concurrent saves", count, err)
	}
}

// faultSource wraps a real Markdown source and lets tests force one
// operation to fail on demand. markdownSource exists precisely for this kind
// of substitution (see its doc comment in markdown_store.go): production
// runs on memory.MarkdownSource, tests wrap it to drive error-propagation
// branches a healthy filesystem and index never exercise.
type faultSource struct {
	inner memory.MarkdownSource
	mu    sync.Mutex

	scanErr   map[memory.Scope]error
	saveErr   error
	deleteErr error

	// armedAfterDelete, when set for a scope, is installed into scanErr for
	// that scope the instant a real delete for that scope succeeds - it
	// simulates the resync immediately following a delete finding the
	// directory has become unreadable.
	armedAfterDelete map[memory.Scope]error
}

func newFaultSource(inner memory.MarkdownSource) *faultSource {
	return &faultSource{inner: inner, scanErr: map[memory.Scope]error{}, armedAfterDelete: map[memory.Scope]error{}}
}

func (f *faultSource) Scan(ctx context.Context, scope memory.Scope) ([]memory.MarkdownDocument, error) {
	f.mu.Lock()
	err := f.scanErr[scope]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.inner.Scan(ctx, scope)
}

func (f *faultSource) Save(ctx context.Context, e memory.Entry) (memory.MarkdownDocument, error) {
	f.mu.Lock()
	err := f.saveErr
	f.mu.Unlock()
	if err != nil {
		return memory.MarkdownDocument{}, err
	}
	return f.inner.Save(ctx, e)
}

func (f *faultSource) Delete(ctx context.Context, path string) error {
	f.mu.Lock()
	err := f.deleteErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if err := f.inner.Delete(ctx, path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, scope := range [2]memory.Scope{memory.ScopeProject, memory.ScopeOrg} {
		dir := f.dirForScope(scope)
		if dir == "" || !strings.HasPrefix(path, dir) {
			continue
		}
		if armed, ok := f.armedAfterDelete[scope]; ok {
			f.scanErr[scope] = armed
		}
	}
	return nil
}

func (f *faultSource) dirForScope(scope memory.Scope) string {
	if scope == memory.ScopeOrg {
		return f.inner.OrgDir()
	}
	return f.inner.ProjectDir()
}

func (f *faultSource) ProjectDir() string { return f.inner.ProjectDir() }
func (f *faultSource) OrgDir() string     { return f.inner.OrgDir() }

func (f *faultSource) setScanErr(scope memory.Scope, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanErr[scope] = err
}

func (f *faultSource) setSaveErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveErr = err
}

func (f *faultSource) setDeleteErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteErr = err
}

func (f *faultSource) armScanErrAfterNextDelete(scope memory.Scope, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.armedAfterDelete[scope] = err
}

// newFaultStore opens a Markdown store over a fault-injecting source with a
// real SQLite index. withOrg configures an organization scope, unpoisoned,
// alongside the project scope so org-branch tests can poison only that
// scope's scan.
func newFaultStore(t *testing.T, readOnly, withOrg bool) (*markdownStore, *faultSource) {
	t.Helper()
	root, indexPath := t.TempDir(), filepath.Join(t.TempDir(), "context.db")
	orgDir, orgID := "", ""
	if withOrg {
		orgDir, orgID = t.TempDir(), "acme"
	}
	source, err := memory.NewMarkdownSource(root, orgDir, orgID)
	if err != nil {
		t.Fatal(err)
	}
	fault := newFaultSource(source)
	index, err := storage.OpenSQLite(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: fault, Index: index, ProjectID: "repo", OrgID: orgID, ReadOnly: readOnly})
	if err != nil {
		t.Fatal(err)
	}
	return store.(*markdownStore), fault
}

func TestOpenMarkdownStoreRequiresIndexAndProjectID(t *testing.T) {
	source, err := memory.NewMarkdownSource(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	t.Run("nil index", func(t *testing.T) {
		if _, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, ProjectID: "repo"}); err == nil {
			t.Fatal("OpenMarkdownStore accepted a nil index")
		}
	})
	t.Run("empty project id", func(t *testing.T) {
		if _, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index}); err == nil {
			t.Fatal("OpenMarkdownStore accepted an empty project ID")
		}
	})
}

func TestOpenMarkdownStorePropagatesOrgScanFailure(t *testing.T) {
	source, err := memory.NewMarkdownSource(t.TempDir(), t.TempDir(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	fault := newFaultSource(source)
	fault.setScanErr(memory.ScopeOrg, errors.New("injected org scan failure"))
	index, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if _, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: fault, Index: index, ProjectID: "repo", OrgID: "acme"}); err == nil {
		t.Fatal("OpenMarkdownStore accepted an organization scope that fails to scan")
	}
}

func TestMarkdownStoreSaveRejectsInvalidEntry(t *testing.T) {
	s, _ := newFaultStore(t, false, false)
	entry := memory.Entry{Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "no title", Why: "test"}
	if _, err := s.Save(context.Background(), entry); err == nil {
		t.Fatal("Save accepted an entry with no title")
	}
}

func TestMarkdownStoreSavePropagatesSourceSaveFailure(t *testing.T) {
	s, fault := newFaultStore(t, false, false)
	fault.setSaveErr(errors.New("injected source save failure"))
	entry := memory.Entry{Title: "x", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "s", Why: "w"}
	if _, err := s.Save(context.Background(), entry); err == nil {
		t.Fatal("Save did not propagate a source Save failure")
	}
}

func TestMarkdownStoreSavePropagatesResyncFailure(t *testing.T) {
	s, fault := newFaultStore(t, false, false)
	fault.setScanErr(memory.ScopeProject, errors.New("injected resync failure"))
	entry := memory.Entry{Title: "x", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "s", Why: "w"}
	if _, err := s.Save(context.Background(), entry); err == nil {
		t.Fatal("Save did not propagate a post-save resync failure")
	}
}

func TestMarkdownStoreSearchPropagatesOrgRefreshFailure(t *testing.T) {
	s, fault := newFaultStore(t, false, true)
	fault.setScanErr(memory.ScopeOrg, errors.New("injected org refresh failure"))
	q := memory.Query{Text: "anything", Scope: memory.ScopeOrg}
	if _, err := s.Search(context.Background(), q); err == nil {
		t.Fatal("Search did not propagate an organization refresh failure")
	}
}

func TestMarkdownStoreSearchRejectsEmptyQueryText(t *testing.T) {
	s, _ := newFaultStore(t, false, false)
	q := memory.Query{Text: "", Scope: memory.ScopeProject}
	if _, err := s.Search(context.Background(), q); err == nil {
		t.Fatal("Search accepted empty query text")
	}
}

func TestMarkdownStoreCountPropagatesRefreshFailure(t *testing.T) {
	s, fault := newFaultStore(t, false, false)
	fault.setScanErr(memory.ScopeProject, errors.New("injected refresh failure"))
	if _, err := s.Count(context.Background(), memory.ScopeProject); err == nil {
		t.Fatal("Count did not propagate a refresh failure")
	}
}

func TestMarkdownStorePromoteToCoreRejectsReadOnly(t *testing.T) {
	s, _ := newFaultStore(t, true, false)
	if err := s.PromoteToCore(context.Background(), "any"); err == nil {
		t.Fatal("PromoteToCore succeeded on a read-only store")
	}
}

func TestMarkdownStorePromoteToCorePropagatesRefreshFailures(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		s, fault := newFaultStore(t, false, false)
		fault.setScanErr(memory.ScopeProject, errors.New("injected project refresh failure"))
		if err := s.PromoteToCore(context.Background(), "any"); err == nil {
			t.Fatal("PromoteToCore did not propagate a project refresh failure")
		}
	})
	t.Run("org", func(t *testing.T) {
		s, fault := newFaultStore(t, false, true)
		fault.setScanErr(memory.ScopeOrg, errors.New("injected org refresh failure"))
		if err := s.PromoteToCore(context.Background(), "any"); err == nil {
			t.Fatal("PromoteToCore did not propagate an organization refresh failure")
		}
	})
}

func TestMarkdownStoreCoreEntriesRejectsInvalidScope(t *testing.T) {
	s, _ := newFaultStore(t, false, false)
	if _, err := s.CoreEntries(context.Background(), memory.ScopeAll); err == nil {
		t.Fatal(`CoreEntries accepted scope "all"`)
	}
}

func TestMarkdownStoreCoreEntriesPropagatesRefreshFailure(t *testing.T) {
	s, fault := newFaultStore(t, false, false)
	fault.setScanErr(memory.ScopeProject, errors.New("injected refresh failure"))
	if _, err := s.CoreEntries(context.Background(), memory.ScopeProject); err == nil {
		t.Fatal("CoreEntries did not propagate a refresh failure")
	}
}

func TestMarkdownStoreDeleteRejectsReadOnly(t *testing.T) {
	s, _ := newFaultStore(t, true, false)
	if err := s.Delete(context.Background(), "any"); err == nil {
		t.Fatal("Delete succeeded on a read-only store")
	}
}

func TestMarkdownStoreDeletePropagatesRefreshFailures(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		s, fault := newFaultStore(t, false, false)
		fault.setScanErr(memory.ScopeProject, errors.New("injected project refresh failure"))
		if err := s.Delete(context.Background(), "any"); err == nil {
			t.Fatal("Delete did not propagate a project refresh failure")
		}
	})
	t.Run("org", func(t *testing.T) {
		s, fault := newFaultStore(t, false, true)
		fault.setScanErr(memory.ScopeOrg, errors.New("injected org refresh failure"))
		if err := s.Delete(context.Background(), "any"); err == nil {
			t.Fatal("Delete did not propagate an organization refresh failure")
		}
	})
}

func TestMarkdownStoreDeleteMapsNotFound(t *testing.T) {
	s, _ := newFaultStore(t, false, false)
	err := s.Delete(context.Background(), "does-not-exist")
	if !errors.Is(err, memory.ErrEntryNotFound) {
		t.Fatalf("Delete error = %v, want memory.ErrEntryNotFound", err)
	}
}

func TestMarkdownStoreDeletePropagatesSourceDeleteFailure(t *testing.T) {
	s, fault := newFaultStore(t, false, false)
	entry := memory.Entry{Title: "x", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "s", Why: "w"}
	result, err := s.Save(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	fault.setDeleteErr(errors.New("injected delete failure"))
	if err := s.Delete(context.Background(), result.ID); err == nil {
		t.Fatal("Delete did not propagate a source Delete failure")
	}
}

func TestMarkdownStoreDeletePropagatesPostDeleteResyncFailure(t *testing.T) {
	s, fault := newFaultStore(t, false, false)
	entry := memory.Entry{Title: "x", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "s", Why: "w"}
	result, err := s.Save(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	fault.armScanErrAfterNextDelete(memory.ScopeProject, errors.New("injected post-delete resync failure"))
	if err := s.Delete(context.Background(), result.ID); err == nil {
		t.Fatal("Delete did not propagate a post-delete resync failure")
	}
}
