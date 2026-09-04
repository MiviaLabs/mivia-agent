package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
