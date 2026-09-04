package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MarkdownDocument is one memory source file and its parsed content.
type MarkdownDocument struct {
	Path  string
	Hash  string
	ID    string
	Entry Entry
}

// MarkdownSource owns the Markdown files for project and organization memory.
// The files are canonical. Any database index is a derived cache.
type MarkdownSource struct {
	projectDir string
	orgDir     string
	orgID      string
}

var memorySlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// NewMarkdownSource creates a source rooted at <project>/.agents/memories and
// the supplied user-level organization memory directory.
func NewMarkdownSource(projectRoot, orgDir, orgID string) (MarkdownSource, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return MarkdownSource{}, errors.New("project root is required")
	}
	if !filepath.IsAbs(projectRoot) {
		return MarkdownSource{}, errors.New("project root must be absolute")
	}
	if orgID != "" {
		normalized, err := NormalizeOrgID(orgID)
		if err != nil {
			return MarkdownSource{}, err
		}
		orgID = normalized
	}
	return MarkdownSource{
		projectDir: filepath.Join(filepath.Clean(projectRoot), ".agents", "memories"),
		orgDir:     filepath.Clean(orgDir),
		orgID:      orgID,
	}, nil
}

// Save validates and atomically writes one source file. The target directory
// is created only after validation succeeds.
func (s MarkdownSource) Save(ctx context.Context, e Entry) (MarkdownDocument, error) {
	if err := contextErr(ctx); err != nil {
		return MarkdownDocument{}, err
	}
	if e.Scope == ScopeOrg && s.orgID == "" {
		return MarkdownDocument{}, errors.New("organization memory requires an org identity")
	}
	if e.Created == "" {
		e.Created = time.Now().Format("2006-01-02")
	}
	if err := e.Validate(Limits{}); err != nil {
		return MarkdownDocument{}, err
	}
	content := []byte(e.Render())
	id := entryID(e.Scope, s.namespace(e.Scope), e.Title, string(content))
	dir, err := s.dir(e.Scope)
	if err != nil {
		return MarkdownDocument{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return MarkdownDocument{}, fmt.Errorf("create memory directory: %w", err)
	}
	path := filepath.Join(dir, slug(e.Title)+"-"+id+".md")
	if err := atomicWrite(ctx, path, content); err != nil {
		return MarkdownDocument{}, err
	}
	return document(path, e, content), nil
}

// Scan returns all regular Markdown files in one scope. It does not recurse
// into the archive directory.
func (s MarkdownSource) Scan(ctx context.Context, scope Scope) ([]MarkdownDocument, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	dir, err := s.dir(scope)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read memory directory: %w", err)
	}
	docs := make([]MarkdownDocument, 0, len(entries))
	for _, item := range entries {
		if item.IsDir() || filepath.Ext(item.Name()) != ".md" {
			continue
		}
		path := filepath.Join(dir, item.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read memory %s: %w", path, err)
		}
		e, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse memory %s: %w", path, err)
		}
		if e.Scope != scope {
			return nil, fmt.Errorf("memory %s declares scope %q, want %q", path, e.Scope, scope)
		}
		docs = append(docs, document(path, e, data))
	}
	return docs, nil
}

// Delete removes one file under a configured memory root.
func (s MarkdownSource) Delete(ctx context.Context, path string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	if !s.inRoot(clean, s.projectDir) && !s.inRoot(clean, s.orgDir) {
		return errors.New("memory path is outside configured roots")
	}
	if filepath.Ext(clean) != ".md" {
		return errors.New("memory path must have .md extension")
	}
	if err := os.Remove(clean); err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	return nil
}

func (s MarkdownSource) dir(scope Scope) (string, error) {
	switch scope {
	case ScopeProject:
		return s.projectDir, nil
	case ScopeOrg:
		if s.orgDir == "." || s.orgDir == "" || s.orgID == "" {
			return "", errors.New("organization memory directory is not configured")
		}
		return s.orgDir, nil
	default:
		return "", fmt.Errorf("unsupported memory scope %q", scope)
	}
}

func (s MarkdownSource) namespace(scope Scope) string {
	if scope == ScopeOrg {
		return s.orgID
	}
	return ""
}

func (s MarkdownSource) inRoot(path, root string) bool {
	if root == "" || root == "." {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func document(path string, e Entry, data []byte) MarkdownDocument {
	hash := sha256.Sum256(data)
	return MarkdownDocument{Path: path, Hash: hex.EncodeToString(hash[:]), ID: documentID(path, e, data), Entry: e}
}

func documentID(path string, e Entry, data []byte) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if idx := strings.LastIndexByte(base, '-'); idx >= 0 && len(base) > idx+1 {
		return base[idx+1:]
	}
	return entryID(e.Scope, "", e.Title, string(data))
}

func slug(title string) string {
	slug := strings.ToLower(strings.TrimSpace(title))
	slug = memorySlugChars.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "memory"
	}
	return slug
}

func atomicWrite(ctx context.Context, path string, data []byte) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-*.tmp")
	if err != nil {
		return fmt.Errorf("create memory temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect memory temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write memory: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync memory: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close memory temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace memory: %w", err)
	}
	return nil
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
