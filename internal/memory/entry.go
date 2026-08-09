// Package memory implements durable agent memory: project-scoped and
// org-scoped entries with a strict Markdown format. Storage is SQLite by
// default, with an in-memory backend for tests and ephemeral sessions.
//
// One entry is one row; the content column holds the rendered Markdown. The
// format is the contract: clean, tidy, concrete entries with a title, a short
// summary, what worked, what did not work, and why. Agents write entries
// through the memory_save and memory_search tools; humans can read the same
// Markdown directly.
package memory

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Scope is the visibility scope of a memory.
type Scope string

const (
	// ScopeProject scopes a memory to one workspace. The project store is a
	// per-workspace database, so it never leaks into other projects.
	ScopeProject Scope = "project"
	// ScopeOrg scopes a memory to the configured org. The org store is a
	// user-level database shared by every project of that org on this machine.
	ScopeOrg Scope = "org"
	// ScopeAll selects both scopes in a search.
	ScopeAll Scope = "all"
)

// Verdict is the agent's assessment of the recorded experience.
type Verdict string

const (
	VerdictGood    Verdict = "good"
	VerdictBad     Verdict = "bad"
	VerdictMixed   Verdict = "mixed"
	VerdictNeutral Verdict = "neutral"
)

// Entry is one memory. Render produces the stored Markdown; Parse reads it
// back tolerantly.
type Entry struct {
	Title      string
	Scope      Scope
	Verdict    Verdict
	Tags       []string
	Created    string // YYYY-MM-DD; empty means "today" at save time
	Summary    string
	Good       string
	Bad        string
	Why        string
	References []string
}

// Limits bounds one entry. Zero values use the defaults.
type Limits struct {
	// MaxEntryBytes caps the rendered entry size. Default 8192.
	MaxEntryBytes int
	// BlockPatterns are regexes; content matching any of them is refused.
	// Configuration-only, like the privacy redaction patterns: nothing is
	// compiled into the binary.
	BlockPatterns []string
}

// Limits defaults. The rendered template stays small by design: a memory is a
// digest of a learning, not a document.
const (
	DefaultMaxEntryBytes = 8192

	maxTitleLen      = 120
	maxSummaryLen    = 400
	maxWhyLen        = 1000
	maxBodyFieldLen  = 2000
	maxTags          = 8
	maxTagLen        = 32
	maxReferences    = 8
	maxReferenceLen  = 200
	minEntryBytes    = 256
	maxEntryBytesCap = 65536
)

// Validate checks the entry against the limits. It refuses control
// characters (except LF and TAB), oversized fields, an oversized rendered
// size, malformed metadata, and content that matches a block pattern.
func (e Entry) Validate(lim Limits) error {
	if err := e.validateMetadata(); err != nil {
		return err
	}
	if err := e.validateBody(); err != nil {
		return err
	}
	if err := e.validateCollections(); err != nil {
		return err
	}
	if err := e.validateSizeAndPatterns(lim); err != nil {
		return err
	}
	return nil
}

func (e Entry) validateMetadata() error {
	title := strings.TrimSpace(e.Title)
	if title == "" {
		return fmt.Errorf("title is required")
	}
	if utf8.RuneCountInString(title) > maxTitleLen {
		return fmt.Errorf("title must be at most %d characters", maxTitleLen)
	}
	if e.Scope != ScopeProject && e.Scope != ScopeOrg {
		return fmt.Errorf("scope must be \"project\" or \"org\", got %q", e.Scope)
	}
	switch e.Verdict {
	case VerdictGood, VerdictBad, VerdictMixed, VerdictNeutral:
	default:
		return fmt.Errorf("verdict must be one of good, bad, mixed, neutral, got %q", e.Verdict)
	}
	if e.Created != "" {
		if _, err := time.Parse("2006-01-02", e.Created); err != nil {
			return fmt.Errorf("created must be YYYY-MM-DD, got %q", e.Created)
		}
	}
	return nil
}

func (e Entry) validateBody() error {
	summary := strings.TrimSpace(e.Summary)
	if summary == "" {
		return fmt.Errorf("summary is required")
	}
	if utf8.RuneCountInString(summary) > maxSummaryLen {
		return fmt.Errorf("summary must be at most %d characters", maxSummaryLen)
	}
	why := strings.TrimSpace(e.Why)
	if why == "" {
		return fmt.Errorf("why is required")
	}
	if utf8.RuneCountInString(why) > maxWhyLen {
		return fmt.Errorf("why must be at most %d characters", maxWhyLen)
	}
	if utf8.RuneCountInString(e.Good) > maxBodyFieldLen {
		return fmt.Errorf("good must be at most %d characters", maxBodyFieldLen)
	}
	if utf8.RuneCountInString(e.Bad) > maxBodyFieldLen {
		return fmt.Errorf("bad must be at most %d characters", maxBodyFieldLen)
	}
	for _, field := range []string{e.Title, e.Summary, e.Good, e.Bad, e.Why} {
		if hasControlChars(field) {
			return fmt.Errorf("content contains a control character")
		}
	}
	return nil
}

func (e Entry) validateCollections() error {
	if len(e.Tags) > maxTags {
		return fmt.Errorf("tags must have at most %d items", maxTags)
	}
	for _, tag := range e.Tags {
		if tag == "" || utf8.RuneCountInString(tag) > maxTagLen {
			return fmt.Errorf("each tag must be 1-%d characters", maxTagLen)
		}
		if hasControlChars(tag) {
			return fmt.Errorf("tag contains a control character")
		}
	}
	if len(e.References) > maxReferences {
		return fmt.Errorf("references must have at most %d items", maxReferences)
	}
	for _, ref := range e.References {
		if ref == "" || utf8.RuneCountInString(ref) > maxReferenceLen {
			return fmt.Errorf("each reference must be 1-%d characters", maxReferenceLen)
		}
		if hasControlChars(ref) {
			return fmt.Errorf("reference contains a control character")
		}
	}
	return nil
}

func (e Entry) validateSizeAndPatterns(lim Limits) error {
	maxBytes := lim.MaxEntryBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxEntryBytes
	}
	if rendered := len([]byte(e.Render())); rendered > maxBytes {
		return fmt.Errorf("entry is %d bytes, exceeds the %d byte limit", rendered, maxBytes)
	}
	if len(lim.BlockPatterns) > 0 {
		content := e.Render()
		for _, pattern := range lim.BlockPatterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("invalid block pattern %q: %w", pattern, err)
			}
			if re.MatchString(content) {
				return fmt.Errorf("refused: content matches a blocked pattern")
			}
		}
	}
	return nil
}

// hasControlChars reports whether s contains a C0 control character other
// than LF (0x0A) and TAB (0x09).
func hasControlChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 && s[i] != '\n' && s[i] != '\t' {
			return true
		}
	}
	return false
}

// Render returns the stored Markdown for the entry. Callers validate first;
// Render itself never fails and always emits the strict template.
func (e Entry) Render() string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(strings.TrimSpace(e.Title))
	b.WriteString("\n\n")
	b.WriteString("scope: ")
	b.WriteString(string(e.Scope))
	b.WriteString("\nverdict: ")
	b.WriteString(string(e.Verdict))
	if len(e.Tags) > 0 {
		b.WriteString("\ntags: ")
		b.WriteString(strings.Join(e.Tags, ", "))
	}
	if e.Created != "" {
		b.WriteString("\ncreated: ")
		b.WriteString(e.Created)
	}
	b.WriteString("\n\n## Summary\n")
	b.WriteString(strings.TrimSpace(e.Summary))
	b.WriteString("\n\n## What worked\n")
	writeSection(&b, e.Good)
	b.WriteString("\n## What did not work\n")
	writeSection(&b, e.Bad)
	b.WriteString("\n## Why\n")
	b.WriteString(strings.TrimSpace(e.Why))
	b.WriteString("\n\n## References\n")
	if len(e.References) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, ref := range e.References {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(ref))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func writeSection(b *strings.Builder, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		b.WriteString("- none\n")
		return
	}
	b.WriteString(content)
	b.WriteString("\n")
}

// Parse reads a stored or hand-edited memory document back into an Entry.
// It is tolerant: missing sections become empty fields, unknown header keys
// and extra lines are ignored. It never panics.
func Parse(data []byte) (Entry, error) {
	var e Entry
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n"), "\n")
	// strings.Split with a non-empty separator always yields at least one
	// element, so a len(lines) == 0 guard would be dead code.
	title := strings.TrimSpace(strings.TrimPrefix(lines[0], "#"))
	if title == "" {
		return e, nil
	}
	e.Title = title
	// Header block: key: value lines between the title and the first blank
	// line that precedes the first section heading.
	section := ""
	inHeader := true
	var body []string
	for _, raw := range lines[1:] {
		trimmed := strings.TrimSpace(raw)
		if inHeader {
			if strings.HasPrefix(trimmed, "## ") {
				inHeader = false
				section = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
				continue
			}
			if trimmed == "" {
				// Blank lines may separate the title from the header and the
				// header from the body; keep reading header keys until the
				// first section heading.
				continue
			}
			key, value, ok := strings.Cut(trimmed, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "scope":
				e.Scope = Scope(value)
			case "verdict":
				e.Verdict = Verdict(value)
			case "created":
				e.Created = value
			case "tags":
				for _, tag := range strings.Split(value, ",") {
					if tag = strings.TrimSpace(tag); tag != "" {
						e.Tags = append(e.Tags, tag)
					}
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			if section != "" && len(body) > 0 {
				assignSection(&e, section, strings.Join(body, "\n"))
			}
			section = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			body = nil
			continue
		}
		if strings.HasPrefix(trimmed, "- ") && section == "References" {
			if ref := strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")); ref != "" && ref != "none" {
				e.References = append(e.References, ref)
			}
			continue
		}
		body = append(body, trimmed)
	}
	if section != "" && len(body) > 0 {
		assignSection(&e, section, strings.Join(body, "\n"))
	}
	return e, nil
}

func assignSection(e *Entry, section, content string) {
	content = strings.TrimSpace(content)
	if content == "" || content == "- none" {
		content = ""
	}
	switch strings.ToLower(section) {
	case "summary":
		e.Summary = content
	case "what worked":
		e.Good = content
	case "what did not work":
		e.Bad = content
	case "why":
		e.Why = content
	}
}
