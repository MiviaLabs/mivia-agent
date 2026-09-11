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

// Importance is how much a memory should shape future work, the same
// closed vocabulary .agents/memories/README.md's frontmatter schema
// requires (scripts/check_memories.py's IMPORTANCE_VALUES) and a distinct
// axis from Verdict: Verdict judges how the recorded experience went,
// Importance judges how much weight a reader should give it.
type Importance string

const (
	ImportanceHigh   Importance = "high"
	ImportanceMedium Importance = "medium"
	ImportanceLow    Importance = "low"
)

// Entry is one memory. Render produces the stored Markdown; Parse reads it
// back tolerantly.
type Entry struct {
	Title      string
	Scope      Scope
	Verdict    Verdict
	Importance Importance // optional; RenderProtocolFile defaults to medium
	Tags       []string
	Related    []string // optional related memory ids (.agents/memories protocol)
	Created    string   // YYYY-MM-DD; empty means "today" at save time
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

// Clamp returns a copy of e with every free-text field truncated to its
// rune limit, so a save never fails on an over-length field. It is the
// lenient counterpart to Validate: agents routinely over-shoot the summary
// (400), why (1000), title (120), and body (2000) limits, and a hard
// rejection just makes them retry the same long text. Clamp keeps the
// leading content (the most informative part) and drops the tail. It never
// changes scope, verdict, tags, or references, and it never makes a field
// empty that was non-empty.
func (e Entry) Clamp() Entry {
	clamped, _ := e.ClampWithReport()
	return clamped
}

// ClampWithReport returns a copy of e with every free-text field truncated to its
// rune limit, along with a slice of field names that were truncated.
func (e Entry) ClampWithReport() (Entry, []string) {
	var truncated []string
	check := func(name, s string, max int) string {
		out := truncateRunes(s, max)
		if len(out) < len(s) {
			truncated = append(truncated, name)
		}
		return out
	}
	e.Title = check("title", e.Title, maxTitleLen)
	e.Summary = check("summary", e.Summary, maxSummaryLen)
	e.Why = check("why", e.Why, maxWhyLen)
	e.Good = check("good", e.Good, maxBodyFieldLen)
	e.Bad = check("bad", e.Bad, maxBodyFieldLen)
	return e, truncated
}

// truncateRunes returns the longest prefix of s that is at most max runes,
// never splitting a UTF-8 rune. When s is already within the limit it is
// returned unchanged.
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	end := 0
	for i := 0; i < max; i++ {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return s[:end]
}

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
	if hasLineControl(title) {
		return fmt.Errorf("title must not contain line breaks")
	}
	if e.Scope != ScopeProject && e.Scope != ScopeOrg {
		return fmt.Errorf("scope must be \"project\" or \"org\", got %q", e.Scope)
	}
	switch e.Verdict {
	case VerdictGood, VerdictBad, VerdictMixed, VerdictNeutral:
	default:
		return fmt.Errorf("verdict must be one of good, bad, mixed, neutral, got %q", e.Verdict)
	}
	switch e.Importance {
	case "", ImportanceHigh, ImportanceMedium, ImportanceLow:
	default:
		return fmt.Errorf("importance must be one of high, medium, low, got %q", e.Importance)
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
	for _, field := range []string{e.Summary, e.Good, e.Bad, e.Why} {
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
		if hasLineControl(tag) {
			return fmt.Errorf("tag must not contain line breaks")
		}
		if strings.Contains(tag, ",") {
			return fmt.Errorf("tag must not contain a comma")
		}
		// RenderProtocolFile writes tags into an unquoted YAML flow sequence
		// ("tags: [a, b]"), and the memories gate (scripts/check_memories.py)
		// requires each element to be a plain keyword. Refuse the characters
		// that break either the sequence or the gate's per-tag rule, so a file
		// this package writes is never rejected downstream.
		if strings.ContainsAny(tag, ":[]{}") || strings.Contains(tag, " #") {
			return fmt.Errorf("tag must be a plain keyword without any of : [ ] { } or \" #\"")
		}
	}
	if len(e.References) > maxReferences {
		return fmt.Errorf("references must have at most %d items", maxReferences)
	}
	for _, ref := range e.References {
		if ref == "" || utf8.RuneCountInString(ref) > maxReferenceLen {
			return fmt.Errorf("each reference must be 1-%d characters", maxReferenceLen)
		}
		if hasLineControl(ref) {
			return fmt.Errorf("reference must not contain line breaks")
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

// hasLineControl reports whether s contains a control character INCLUDING LF
// and TAB. Title, tags, and references are stored on one line, so a line
// break in them would corrupt the rendered template and the Parse round-trip.
func hasLineControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
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
	b.WriteString("\n\n")
	writeBody(&b, e)
	return b.String()
}

// RenderProtocolFile returns the Markdown MarkdownSource.Save writes to
// .agents/memories/<id>.md: a closed YAML frontmatter block carrying the six
// keys .agents/memories/README.md marks mandatory (id, title, content,
// importance, tags, updated - enforced by scripts/check_memories.py), the
// same open ".agents protocol" shape a hand-authored, capture-skill-written
// memory uses, followed by the same title-and-section body Render's own
// template emits. id must be the caller's resolved filename stem with every
// hyphen replaced by an underscore (README's derivation rule); the caller
// computes it, since only MarkdownSource knows the final filename.
//
// title and content (the entry's Summary, one sentence) are wrapped in
// single quotes and any embedded single quote is doubled, the one escaping
// scripts/check_memories.py's QUOTED rule accepts unconditionally - simpler
// and safer than quoting only when a colon or indicator character is
// present. importance falls back to "medium" when the entry does not carry
// one, so a file this method writes never has an empty importance field,
// the one required key Entry has no dedicated field for.
func (e Entry) RenderProtocolFile(id string) string {
	importance := strings.TrimSpace(string(e.Importance))
	if importance == "" {
		importance = string(ImportanceMedium)
	}
	updated := e.Created
	if updated == "" {
		updated = time.Now().Format("2006-01-02")
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: ")
	b.WriteString(id)
	b.WriteString("\ntitle: ")
	b.WriteString(yamlSingleQuote(strings.TrimSpace(e.Title)))
	b.WriteString("\ncontent: ")
	b.WriteString(yamlSingleQuote(oneLine(e.Summary)))
	b.WriteString("\nimportance: ")
	b.WriteString(importance)
	// x-scope/x-verdict are not among the six keys README.md marks
	// mandatory, but scripts/check_memories.py places no ceiling on extra
	// keys, and parseProtocolMemory (markdown_source.go) reads them back
	// when present. Without them, every file this method writes would
	// silently forget the caller's actual Scope/Verdict on the next Scan -
	// parseProtocolMemory's only other source for those fields is the
	// hardcoded ScopeProject/VerdictNeutral fallback for a hand-authored
	// file that never carried them at all. The "x-" prefix (rather than a
	// bare "scope"/"verdict") is deliberate: Parse's own header switch
	// recognizes unprefixed "scope:"/"verdict:" lines and would set
	// e.Scope/e.Verdict from THIS frontmatter block before Scan ever
	// reaches the protocol shape below, taking the wrong (Parse's own
	// legacy-header) branch entirely - Scan only falls through to
	// parseProtocolMemory when Parse's own e.Scope comes back empty.
	b.WriteString("\nx-scope: ")
	b.WriteString(string(e.Scope))
	b.WriteString("\nx-verdict: ")
	b.WriteString(string(e.Verdict))
	b.WriteString("\ntags: [")
	if len(e.Tags) > 0 {
		b.WriteString(strings.Join(e.Tags, ", "))
	} else {
		// tags is a required, non-empty key; scope is always a real tag
		// (project or org) and never absent, so it is a safe fallback that
		// carries real information instead of a placeholder.
		b.WriteString(string(e.Scope))
	}
	b.WriteString("]")
	if len(e.Related) > 0 {
		b.WriteString("\nrelated: [")
		b.WriteString(strings.Join(e.Related, ", "))
		b.WriteString("]")
	}
	b.WriteString("\nupdated: ")
	b.WriteString(updated)
	b.WriteString("\n---\n\n# ")
	b.WriteString(strings.TrimSpace(e.Title))
	b.WriteString("\n\n")
	writeBody(&b, e)
	return b.String()
}

// yamlSingleQuote wraps s in single quotes, doubling any embedded single
// quote - the one YAML escaping scripts/check_memories.py's QUOTED regex
// accepts for every value, including one that opens with a YAML indicator
// character or holds an unescaped colon.
func yamlSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// yamlUnquote reverses yamlSingleQuote: a value wrapped in single quotes is
// unwrapped and every doubled ” collapses back to '. A value not wrapped in
// matching single quotes (a hand-authored file's unquoted title, or one
// quoted with double quotes) passes through unchanged - parseProtocolMemory
// applies this to every frontmatter value, and only this package's own
// RenderProtocolFile ever emits the single-quoted form.
func yamlUnquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}

// oneLine collapses s onto a single line: a YAML plain or single-quoted
// scalar cannot carry a literal newline. content is Entry.Summary, already
// bounded to one to three sentences by Validate, so joining is lossless in
// practice.
func oneLine(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// writeBody writes the section template Render and RenderProtocolFile share:
// Summary, What worked, What did not work, Why, References. Both callers
// have already written the entry's title and header/frontmatter block.
func writeBody(b *strings.Builder, e Entry) {
	b.WriteString("## Summary\n")
	b.WriteString(strings.TrimSpace(e.Summary))
	b.WriteString("\n\n## What worked\n")
	writeSection(b, e.Good)
	b.WriteString("\n## What did not work\n")
	writeSection(b, e.Bad)
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
			case "importance":
				e.Importance = Importance(value)
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
	case "history", "archive note":
		if e.Why != "" {
			e.Why = e.Why + "\n\n## " + section + "\n" + content
		} else {
			e.Why = content
		}
	}
}
