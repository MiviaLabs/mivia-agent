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
	if strings.TrimSpace(orgDir) != "" && !filepath.IsAbs(orgDir) {
		return MarkdownSource{}, errors.New("organization memory directory must be absolute")
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
	// id is the filename's uniqueness suffix, hashed from the legacy Render
	// shape purely for a stable dedup key; it has no bearing on what gets
	// written to disk (below) and changing the on-disk format must not
	// change this derivation, since document()/documentID() and existing
	// callers key off the filename's <slug>-<id> shape.
	id := entryID(e.Scope, s.namespace(e.Scope), e.Title, e.Render())
	stem := slug(e.Title) + "-" + id
	dir, err := s.dir(e.Scope)
	if err != nil {
		return MarkdownDocument{}, err
	}
	if err := rejectSymlinkComponents(dir); err != nil {
		return MarkdownDocument{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return MarkdownDocument{}, fmt.Errorf("create memory directory: %w", err)
	}
	if err := rejectSymlinkComponents(dir); err != nil {
		return MarkdownDocument{}, err
	}

	// Check for a near-duplicate among existing documents in the scope.
	// If one exists, merge into the existing document rather than writing a duplicate file.
	if doc, merged, err := s.mergeSimilar(ctx, e); err != nil {
		return MarkdownDocument{}, err
	} else if merged {
		return doc, nil
	}
	// .agents/memories/README.md derives a file's frontmatter id from its
	// filename: drop .md, replace every hyphen with an underscore
	// (scripts/check_memories.py's expected_id). RenderProtocolFile writes
	// that same id into the frontmatter so a file this package writes
	// passes the pre-push gate the capture skill's own files must pass.
	protocolID := strings.ReplaceAll(stem, "-", "_")
	content := []byte(e.RenderProtocolFile(protocolID))
	path := filepath.Join(dir, stem+".md")
	if err := atomicWrite(ctx, path, content); err != nil {
		return MarkdownDocument{}, err
	}
	doc := document(path, e, content)
	// A file this method writes always re-parses through parseProtocolMemory
	// on the next Scan (its "id:" frontmatter key sets a value Parse's own
	// legacy-header path never populates), which reports doc.ID as that
	// frontmatter id, not documentID's filename-hash-suffix convention. The
	// two must agree here so a caller that saves and later looks the entry
	// up by the ID Save just returned - memory_delete keyed on
	// memory_save's own result, for one - still finds it after a Scan.
	doc.ID = protocolID
	return doc, nil
}

func (s MarkdownSource) mergeSimilar(ctx context.Context, e Entry) (MarkdownDocument, bool, error) {
	if err := contextErr(ctx); err != nil {
		return MarkdownDocument{}, false, err
	}
	existingDocs, err := s.Scan(ctx, e.Scope)
	if err != nil {
		return MarkdownDocument{}, false, err
	}
	for _, existing := range existingDocs {
		if EntrySimilarity(existing.Entry, e) >= similarityMergeThreshold {
			merged := MergeEntries(existing.Entry, e).Clamp()
			if merged.Verdict == "" {
				merged.Verdict = VerdictGood
			}
			if merged.Created == "" {
				merged.Created = time.Now().Format("2006-01-02")
			}
			if err := merged.Validate(Limits{}); err != nil {
				// If merged fails validation (e.g. existing had an invalid legacy tag/format),
				// do not fail the save. Skip merging into this entry and fall through to
				// write a new valid file.
				continue
			}
			stem := strings.TrimSuffix(filepath.Base(existing.Path), ".md")
			protocolID := strings.ReplaceAll(stem, "-", "_")
			content := []byte(merged.RenderProtocolFile(protocolID))
			if err := atomicWrite(ctx, existing.Path, content); err != nil {
				return MarkdownDocument{}, false, err
			}
			doc := document(existing.Path, merged, content)
			doc.ID = protocolID
			return doc, true, nil
		}
	}
	return MarkdownDocument{}, false, nil
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
		if item.IsDir() || item.Name() == "README.md" || filepath.Ext(item.Name()) != ".md" {
			continue
		}
		path := filepath.Join(dir, item.Name())
		if item.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("memory source %s is a symbolic link", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read memory %s: %w", path, err)
		}
		e, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse memory %s: %w", path, err)
		}
		if e.Scope == "" {
			legacy, id, ok := parseProtocolMemory(data, scope)
			if ok {
				doc := document(path, legacy, data)
				doc.ID = id
				docs = append(docs, doc)
				continue
			}
			// A memory under the project directory is project-scoped by
			// definition, so an omitted scope field is not an error there.
			if scope == ScopeProject {
				e.Scope = ScopeProject
			}
		}
		// Hand-authored files may omit fields the derived index requires.
		// Reconcile with defaults instead of failing the whole scan.
		if e.Verdict == "" {
			e.Verdict = VerdictNeutral
		}
		if e.Scope != scope {
			return nil, fmt.Errorf("memory %s declares scope %q, want %q", path, e.Scope, scope)
		}
		docs = append(docs, document(path, e, data))
	}
	return docs, nil
}

func parseProtocolMemory(data []byte, scope Scope) (Entry, string, bool) {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return Entry{}, "", false
	}
	values := make(map[string]string)
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
		key, value, ok := strings.Cut(lines[i], ":")
		if ok {
			values[strings.TrimSpace(strings.ToLower(key))] = yamlUnquote(strings.TrimSpace(value))
		}
	}
	if end < 0 || values["id"] == "" || values["title"] == "" || values["content"] == "" {
		return Entry{}, "", false
	}
	tags := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(values["tags"], "]"), "["))
	var tagList []string
	for _, tag := range strings.Split(tags, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tagList = append(tagList, tag)
		}
	}
	related := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(values["related"], "]"), "["))
	var relatedList []string
	for _, rel := range strings.Split(related, ",") {
		if rel = strings.TrimSpace(rel); rel != "" {
			relatedList = append(relatedList, rel)
		}
	}
	// Scope is always the directory-implied scan scope, never a value read
	// from the frontmatter: Save always writes into the scope-correct
	// directory, so the two never legitimately disagree for a file this
	// package wrote, and trusting an embedded "scope:" on a hand-edited
	// file risks Scan's scope-mismatch check rejecting the whole scan
	// (markdown_source.go Scan) over a typo in one memory.
	verdict := Verdict(values["x-verdict"])
	switch verdict {
	case VerdictGood, VerdictBad, VerdictMixed, VerdictNeutral:
	default:
		verdict = VerdictNeutral
	}
	e := Entry{
		Title: values["title"], Scope: scope, Verdict: verdict,
		Importance: Importance(values["importance"]), Tags: tagList, Related: relatedList, Summary: values["content"],
		// README's "updated" key is this package's Created field: Save has
		// no separate "last edited" concept (the capture skill never edits
		// an existing memory either), so the two names carry one value.
		Created: values["updated"],
	}
	bodyLines := lines[end+1:]
	// RenderProtocolFile's own body shape - "# <title>" followed by the
	// Summary/What worked/What did not work/Why/References sections Parse
	// already knows how to read - is detected by its leading "# " heading
	// and parsed structurally, so a file this package wrote round-trips
	// through Scan with Good/Bad/References intact instead of collapsing
	// into Why. A hand-authored file without that heading (the capture
	// skill's terse/narrative/incident body shapes have no fixed section
	// grammar) keeps the prior whole-body-as-Why fallback: Parse would
	// otherwise misread its first line as a bogus title and silently drop
	// the rest as unrecognized header lines.
	body := strings.TrimSpace(strings.Join(bodyLines, "\n"))
	e.Why = body
	if parsed, ok := parseFullyAccountedBody(body); ok {
		if parsed.Summary != "" {
			e.Summary = parsed.Summary
		}
		e.Good = parsed.Good
		e.Bad = parsed.Bad
		e.Why = parsed.Why
		e.References = parsed.References
	}
	return e, values["id"], true
}

// parseFullyAccountedBody re-parses a protocol body structurally and reports
// whether the parse ACCOUNTS FOR ALL OF IT: a "# " title followed only by
// sections Parse understands (Summary / What worked / What did not work /
// Why / References), with no free text outside them.
//
// Partial recognition is not enough to adopt. Parse silently drops every
// unrecognized section and every non-header line before the first section, so
// a hand-authored body carrying one "## Why" among its own headings would
// come back stripped of everything else - and a body with a recognized
// section but no "## Why" would come back with Why empty, losing the body
// entirely. Both are worse than keeping the raw body, so anything the parser
// cannot fully account for keeps the whole-body-as-Why fallback.
func parseFullyAccountedBody(body string) (Entry, bool) {
	if !strings.HasPrefix(body, "# ") {
		return Entry{}, false
	}
	parsed, err := Parse([]byte(body))
	if err != nil || parsed.Title == "" {
		return Entry{}, false
	}
	// Walk the body the way Parse does and reject anything it would discard:
	// a heading it does not assign, or prose sitting outside every section.
	lines := strings.Split(body, "\n")
	inSection := false
	for _, raw := range lines[1:] {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if heading, found := strings.CutPrefix(trimmed, "## "); found {
			switch strings.ToLower(strings.TrimSpace(heading)) {
			case "summary", "what worked", "what did not work", "why", "references", "history", "archive note", "prior context":
				inSection = true
				continue
			default:
				return Entry{}, false
			}
		}
		if !inSection {
			// Free text between the title and the first section: Parse reads
			// it as header "key: value" lines and drops the rest.
			return Entry{}, false
		}
	}
	// A title with no recognized section at all carries no structure to adopt.
	if !inSection {
		return Entry{}, false
	}
	return parsed, true
}

// Delete removes one file under a configured memory root.
func (s MarkdownSource) Delete(ctx context.Context, path string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	if err := rejectSymlinkComponents(filepath.Dir(clean)); err != nil {
		return err
	}
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

// ProjectDir returns the project memory directory this source scans:
// <projectRoot>/.agents/memories.
func (s MarkdownSource) ProjectDir() string { return s.projectDir }

// OrgDir returns the organization memory directory this source scans, or an
// empty value when no organization directory is configured.
func (s MarkdownSource) OrgDir() string {
	if s.orgDir == "" || s.orgDir == "." {
		return ""
	}
	return s.orgDir
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
	return syncMemoryDir(filepath.Dir(path))
}

func rejectSymlinkComponents(path string) error {
	clean, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := clean; ; current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("memory source path contains symbolic link: %s", current)
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
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
