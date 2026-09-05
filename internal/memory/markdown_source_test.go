package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validMemoryEntry(title string) Entry {
	return Entry{
		Title: title, Scope: ScopeProject, Verdict: VerdictNeutral,
		Summary: "Summary.", Why: "Why.",
	}
}

func TestMarkdownSourceWritesAndScansProjectMemory(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, filepath.Join(t.TempDir(), "memories"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{
		Title: "Safe cache cleanup",
		Scope: ScopeProject, Verdict: VerdictGood, Created: "2026-09-04",
		Tags: []string{"cache", "safe"}, Summary: "Use a lock before cleanup.",
		Good: "The lock prevents concurrent cleanup.", Why: "The cache is shared.",
	}
	doc, err := source.Save(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.Path, filepath.Join(root, ".agents", "memories")) {
		t.Fatalf("path = %q, want project memory path", doc.Path)
	}
	if doc.ID == "" || doc.Hash == "" {
		t.Fatalf("document identity is empty: %#v", doc)
	}

	docs, err := source.Scan(context.Background(), ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Entry.Title != e.Title || docs[0].ID != doc.ID {
		t.Fatalf("scanned documents = %#v, want saved document", docs)
	}
}

func TestMarkdownSourceScansProtocolMemory(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("---\nid: stable_memory\ntitle: Stable memory\ncontent: Keep this fact.\nimportance: high\ntags: [ops, tests]\n---\n\nThe detail matters.\n")
	if err := os.WriteFile(filepath.Join(dir, "stable-memory.md"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	docs, err := source.Scan(context.Background(), ScopeProject)
	if err != nil || len(docs) != 1 {
		t.Fatalf("docs=%d err=%v, want one protocol memory", len(docs), err)
	}
	if docs[0].ID != "stable_memory" || docs[0].Entry.Summary != "Keep this fact." || len(docs[0].Entry.Tags) != 2 {
		t.Fatalf("protocol document = %+v, want mapped fields", docs[0])
	}
}

func TestMarkdownSourceSeparatesProjectAndOrgFiles(t *testing.T) {
	project, org := t.TempDir(), filepath.Join(t.TempDir(), "org-memories")
	source, err := NewMarkdownSource(project, org, "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{
		{Title: "Project fact", Scope: ScopeProject, Verdict: VerdictNeutral, Summary: "Project.", Why: "Project."},
		{Title: "Org fact", Scope: ScopeOrg, Verdict: VerdictNeutral, Summary: "Org.", Why: "Org."},
	} {
		if _, err := source.Save(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	projectDocs, err := source.Scan(context.Background(), ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	orgDocs, err := source.Scan(context.Background(), ScopeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if len(projectDocs) != 1 || len(orgDocs) != 1 {
		t.Fatalf("project=%d org=%d, want one document in each scope", len(projectDocs), len(orgDocs))
	}
}

func TestMarkdownSourceDeleteRejectsPathOutsideRoots(t *testing.T) {
	project := t.TempDir()
	source, err := NewMarkdownSource(project, filepath.Join(t.TempDir(), "org"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "memory.md")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := source.Delete(context.Background(), outside); err == nil {
		t.Fatal("Delete accepted a path outside the configured roots")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was changed: %v", err)
	}
}

func TestNewMarkdownSourceRejectsRelativeOrganizationDirectory(t *testing.T) {
	if _, err := NewMarkdownSource(t.TempDir(), "relative-memories", "acme"); err == nil {
		t.Fatal("NewMarkdownSource accepted a relative organization directory")
	}
}

func TestMarkdownSourceDirAccessors(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(t.TempDir(), "org-memories")
	source, err := NewMarkdownSource(root, orgDir, "acme")
	if err != nil {
		t.Fatal(err)
	}
	wantProject := filepath.Join(root, ".agents", "memories")
	if got := source.ProjectDir(); got != wantProject {
		t.Fatalf("ProjectDir() = %q, want %q", got, wantProject)
	}
	if got := source.OrgDir(); got != orgDir {
		t.Fatalf("OrgDir() = %q, want %q", got, orgDir)
	}
	bare, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := bare.ProjectDir(); got != wantProject {
		t.Fatalf("ProjectDir() = %q, want %q", got, wantProject)
	}
	if got := bare.OrgDir(); got != "" {
		t.Fatalf("OrgDir() = %q, want empty when no organization directory is configured", got)
	}
}

func TestNewMarkdownSourceRejectsEmptyOrRelativeProjectRoot(t *testing.T) {
	if _, err := NewMarkdownSource("", "", ""); err == nil {
		t.Fatal("NewMarkdownSource accepted an empty project root")
	}
	if _, err := NewMarkdownSource("relative/project", "", ""); err == nil {
		t.Fatal("NewMarkdownSource accepted a relative project root")
	}
}

func TestNewMarkdownSourceRejectsInvalidOrgID(t *testing.T) {
	// NormalizeOrgID rejects a leading slash; propagating that error is
	// what line 49 (`return MarkdownSource{}, err`) exists for.
	if _, err := NewMarkdownSource(t.TempDir(), "", "/leading-slash"); err == nil {
		t.Fatal("NewMarkdownSource accepted an org ID NormalizeOrgID rejects")
	}
}

func TestMarkdownSourceSaveRejectsCanceledContext(t *testing.T) {
	source, err := NewMarkdownSource(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Save(ctx, validMemoryEntry("x")); err == nil {
		t.Fatal("Save accepted a canceled context")
	}
}

func TestMarkdownSourceSaveRejectsOrgScopeWithoutOrgID(t *testing.T) {
	source, err := NewMarkdownSource(t.TempDir(), t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	e := validMemoryEntry("x")
	e.Scope = ScopeOrg
	if _, err := source.Save(context.Background(), e); err == nil {
		t.Fatal("Save accepted ScopeOrg with no configured org identity")
	}
}

func TestMarkdownSourceSaveRejectsInvalidEntry(t *testing.T) {
	source, err := NewMarkdownSource(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Save(context.Background(), validMemoryEntry("")); err == nil {
		t.Fatal("Save accepted an entry with no title")
	}
}

func TestMarkdownSourceSaveOrgScopeWithOrgIDButNoOrgDir(t *testing.T) {
	// orgID configured (so Save's own ScopeOrg guard passes) but orgDir left
	// empty (Clean()s to "."): the failure moves one level down, into
	// s.dir's own "organization memory directory is not configured" check.
	source, err := NewMarkdownSource(t.TempDir(), "", "acme")
	if err != nil {
		t.Fatal(err)
	}
	e := validMemoryEntry("x")
	e.Scope = ScopeOrg
	if _, err := source.Save(context.Background(), e); err == nil {
		t.Fatal("Save accepted ScopeOrg with an org identity but no org directory")
	}
}

func TestMarkdownSourceDirRejectsUnsupportedScope(t *testing.T) {
	source, err := NewMarkdownSource(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Scan calls s.dir directly, unlike Save (which Entry.Validate gates to
	// ScopeProject/ScopeOrg first) - this is the one path that can reach
	// dir()'s default case.
	if _, err := source.Scan(context.Background(), Scope("bogus")); err == nil {
		t.Fatal("Scan accepted an unsupported scope")
	}
}

func TestMarkdownSourceSaveRejectsSymlinkedMemoryDir(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	memDir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(filepath.Dir(memDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, memDir); err != nil {
		t.Fatal(err)
	}
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Save(context.Background(), validMemoryEntry("x")); err == nil {
		t.Fatal("Save accepted a symlinked memory directory")
	}
}

func TestMarkdownSourceSaveMkdirAllFailsOnNonDirectory(t *testing.T) {
	root := t.TempDir()
	// Pre-create a regular FILE at the exact path Save wants to mkdir - the
	// mkdir then fails because the path exists and is not a directory.
	memDir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(filepath.Dir(memDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Save(context.Background(), validMemoryEntry("x")); err == nil {
		t.Fatal("Save accepted a memory directory path that is actually a file")
	}
}

func TestMarkdownSourceScanRejectsCanceledContext(t *testing.T) {
	source, err := NewMarkdownSource(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Scan(ctx, ScopeProject); err == nil {
		t.Fatal("Scan accepted a canceled context")
	}
}

func TestMarkdownSourceScanDirError(t *testing.T) {
	source, err := NewMarkdownSource(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Scan(context.Background(), ScopeOrg); err == nil {
		t.Fatal("Scan(ScopeOrg) accepted an unconfigured organization directory")
	}
}

func TestMarkdownSourceScanRejectsSymlinkedFile(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Save(context.Background(), validMemoryEntry("real entry")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("# linked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".agents", "memories", "linked.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Scan(context.Background(), ScopeProject); err == nil {
		t.Fatal("Scan accepted a symlinked .md file")
	}
}

func TestMarkdownSourceScanReadFileError(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(dir, "unreadable.md")
	if err := os.WriteFile(unreadable, []byte("# secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	if _, err := source.Scan(context.Background(), ScopeProject); err == nil {
		t.Fatal("Scan accepted a file it cannot read")
	}
}

func TestMarkdownSourceDeleteRejectsCanceledContext(t *testing.T) {
	source, err := NewMarkdownSource(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.Delete(ctx, "whatever.md"); err == nil {
		t.Fatal("Delete accepted a canceled context")
	}
}

func TestMarkdownSourceDeleteRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	memDir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(filepath.Dir(memDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, memDir); err != nil {
		t.Fatal(err)
	}
	if err := source.Delete(context.Background(), filepath.Join(memDir, "x.md")); err == nil {
		t.Fatal("Delete accepted a path through a symlinked parent")
	}
}

func TestMarkdownSourceDeleteRejectsNonMarkdownExtension(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	notMD := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notMD, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := source.Delete(context.Background(), notMD); err == nil {
		t.Fatal("Delete accepted a non-.md extension")
	}
	if _, statErr := os.Stat(notMD); statErr != nil {
		t.Fatalf("rejected file was removed anyway: %v", statErr)
	}
}

func TestMarkdownSourceDeleteRemoveError(t *testing.T) {
	root := t.TempDir()
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := source.Save(context.Background(), validMemoryEntry("gone twice"))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Delete(context.Background(), doc.Path); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	// The file no longer exists: os.Remove itself must now fail.
	if err := source.Delete(context.Background(), doc.Path); err == nil {
		t.Fatal("second Delete on an already-removed file did not error")
	}
}

func TestMarkdownSourceInRootRejectsUnconfiguredRoot(t *testing.T) {
	source, err := NewMarkdownSource(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if source.inRoot("/anything", "") {
		t.Fatal("inRoot accepted an empty root")
	}
	if source.inRoot("/anything", ".") {
		t.Fatal("inRoot accepted \".\" as a root")
	}
}

func TestDocumentIDFallsBackWhenFilenameHasNoHyphen(t *testing.T) {
	e := validMemoryEntry("no hyphen source")
	got := documentID("/memories/nohyphen.md", e, []byte("content"))
	want := entryID(e.Scope, "", e.Title, "content")
	if got != want {
		t.Fatalf("documentID = %q, want the entryID fallback %q", got, want)
	}
}

func TestSlugFallsBackToMemoryForAnEmptyResult(t *testing.T) {
	if got := slug("!!!"); got != "memory" {
		t.Fatalf("slug(%q) = %q, want the \"memory\" fallback", "!!!", got)
	}
}

func TestRejectSymlinkComponentsDetectsASymlinkedAncestor(t *testing.T) {
	target := t.TempDir()
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := rejectSymlinkComponents(filepath.Join(link, "file.md")); err == nil {
		t.Fatal("rejectSymlinkComponents did not detect a symlinked ancestor")
	}
}

func TestAtomicWriteRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "x.md")
	if err := atomicWrite(ctx, path, []byte("data")); err == nil {
		t.Fatal("atomicWrite accepted a canceled context")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("atomicWrite wrote a file despite a canceled context")
	}
}

func TestRejectSymlinkComponentsPropagatesANonNotExistLstatError(t *testing.T) {
	// A component name past the OS limit makes Lstat fail with
	// ENAMETOOLONG, not ErrNotExist - the one other error class the loop
	// must propagate instead of treating as "no such ancestor yet".
	tooLong := strings.Repeat("a", 300)
	if err := rejectSymlinkComponents(filepath.Join(t.TempDir(), tooLong)); err == nil {
		t.Fatal("rejectSymlinkComponents did not propagate a non-ErrNotExist Lstat failure")
	}
}

func TestMarkdownSourceSaveAtomicWriteError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses the write-permission check this test relies on")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ".agents", "memories")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	source, err := NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// MkdirAll is a no-op on the already-existing (read-only) dir, so Save
	// reaches atomicWrite, whose os.CreateTemp then fails for lack of
	// write permission - propagated back through Save's own error return.
	if _, err := source.Save(context.Background(), validMemoryEntry("no write perm")); err == nil {
		t.Fatal("Save accepted a memory directory it cannot write into")
	}
}

func TestParseProtocolMemoryRejectsMalformedFrontmatter(t *testing.T) {
	if _, _, ok := parseProtocolMemory([]byte("not frontmatter at all"), ScopeProject); ok {
		t.Fatal("parseProtocolMemory accepted a file with no frontmatter delimiter")
	}
	// Frontmatter present but missing every required key.
	missingKeys := []byte("---\ntags: [a]\n---\nbody\n")
	if _, _, ok := parseProtocolMemory(missingKeys, ScopeProject); ok {
		t.Fatal("parseProtocolMemory accepted frontmatter missing id/title/content")
	}
}
