package skills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxSkillBytes = 256 << 10

const (
	argsHintMaxLen         = 80
	shortDescriptionMaxLen = 60
)

// Source is one skill directory and its provenance. Sources are merged with
// project definitions taking precedence over user definitions of the same name.
type Source struct {
	Dir    string
	Origin Origin
}

// LoadOptions controls resilient multi-source skill discovery.
type LoadOptions struct {
	ReservedNames       map[string]struct{}
	ReservedSlashTokens map[string]struct{}
}

// loadMarkdown loads instruction-only skills from <root>/*/SKILL.md.
// Markdown is passed to the subagent handler as a system instruction; no embedded
// code, shell command, or tool declaration is executed by the loader.
func loadMarkdown(root string) (*Registry, error) {
	registry := NewRegistry()
	if strings.TrimSpace(root) == "" {
		return registry, nil
	}
	skillRoot, err := openSkillRoot(root)
	if os.IsNotExist(err) {
		return registry, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}
	defer skillRoot.Close()
	entries, err := fs.ReadDir(skillRoot.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}
	for _, entry := range entries {
		skillDir, ok, err := openSkillDirectory(skillRoot, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read skill %q: %w", entry.Name(), err)
		}
		if !ok {
			continue
		}
		def, ok, _, err := loadSkillDirAt(skillDir, entry.Name(), filepath.Join(root, entry.Name()), OriginProject)
		skillDir.Close()
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if err := registry.Register(def); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// LoadMarkdownSources merges user and project skill directories. Invalid,
// unreadable, duplicate, and reserved skills are skipped with bounded warnings
// so one user-authored file cannot prevent chat startup.
func LoadMarkdownSources(sources []Source, options LoadOptions) (*Registry, []string, error) {
	registry := NewRegistry()
	warnings := make([]string, 0)
	slashOwners := make(map[string]Origin)
	ordered := append([]Source(nil), sources...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Origin == OriginUser && ordered[j].Origin == OriginProject })
	for _, source := range ordered {
		warnings = append(warnings, loadMarkdownSource(registry, source, options, slashOwners)...)
	}
	return registry, warnings, nil
}

func loadMarkdownSource(registry *Registry, source Source, options LoadOptions, slashOwners map[string]Origin) []string {
	if strings.TrimSpace(source.Dir) == "" {
		return nil
	}
	skillRoot, err := openSkillRoot(source.Dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return []string{"skip skills directory"}
	}
	defer skillRoot.Close()
	entries, err := fs.ReadDir(skillRoot.FS(), ".")
	if err != nil {
		return []string{"skip skills directory"}
	}
	var warnings []string
	for _, entry := range entries {
		skillDir, ok, err := openSkillDirectory(skillRoot, entry.Name())
		if err != nil {
			warnings = append(warnings, "skip invalid skill")
			continue
		}
		if !ok {
			continue
		}
		def, ok, warning, err := loadSkillDirAt(skillDir, entry.Name(), filepath.Join(source.Dir, entry.Name()), source.Origin)
		skillDir.Close()
		if err != nil {
			warnings = append(warnings, "skip invalid skill")
			continue
		}
		if ok {
			if warning != "" {
				warnings = append(warnings, warning)
			}
			warnings = append(warnings, registerMarkdownSkill(registry, def, options, slashOwners)...)
		}
	}
	return warnings
}

func registerMarkdownSkill(registry *Registry, def Definition, options LoadOptions, slashOwners map[string]Origin) []string {
	if _, reserved := options.ReservedNames[def.Name]; reserved {
		return []string{"skip reserved skill"}
	}
	prior, exactNameExists := registry.items[def.Name]
	if exactNameExists && !(def.Origin == OriginProject && prior.Origin == OriginUser) {
		return []string{"skip duplicate skill"}
	}
	warnings := slashEligibilityWarnings(def, exactNameExists, options, slashOwners)
	if exactNameExists {
		warnings = append(warnings, "project skill shadows user skill")
	}
	registry.items[def.Name] = def
	return warnings
}

func slashEligibilityWarnings(def Definition, exactNameExists bool, options LoadOptions, slashOwners map[string]Origin) []string {
	if !def.UserInvocable {
		return nil
	}
	token, eligible := SlashToken(def.Name)
	switch {
	case !eligible:
		return []string{"skip unsluggable slash skill"}
	case hasToken(options.ReservedSlashTokens, token):
		return []string{"skip builtin-colliding slash skill"}
	case slashOwners[token] == "":
		slashOwners[token] = def.Origin
	case def.Origin == OriginProject && slashOwners[token] == OriginUser:
		slashOwners[token] = OriginProject
		if !exactNameExists {
			return []string{"project skill shadows user slash command"}
		}
	case !exactNameExists:
		return []string{"duplicate slash command"}
	}
	return nil
}

// openSkillRoot pins the skill root to a descriptor and rejects a symbolic
// link at its boundary. All later child traversal is descriptor-relative, so
// a concurrent workspace rename cannot redirect discovery outside this root.
func openSkillRoot(path string) (*os.Root, error) {
	clean := filepath.Clean(path)
	parent, err := os.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	base := filepath.Base(clean)
	info, err := parent.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skills directory is not a real directory")
	}
	root, err := parent.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	opened, err := root.Lstat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("skills directory changed while opening")
	}
	return root, nil
}

func openSkillDirectory(root *os.Root, name string) (*os.Root, bool, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, false, nil
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return nil, false, err
	}
	opened, err := dir.Lstat(".")
	if err != nil || !os.SameFile(info, opened) {
		dir.Close()
		if err != nil {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("skill directory changed while opening")
	}
	return dir, true, nil
}

func hasToken(tokens map[string]struct{}, token string) bool {
	_, ok := tokens[token]
	return ok
}

// loadSkillDirAt reads and parses SKILL.md from an already-pinned directory.
// ok is false when the directory holds no SKILL.md, which is not an error.
func loadSkillDirAt(root *os.Root, dir, sourcePath string, origin Origin) (Definition, bool, string, error) {
	data, err := readRegularSkill(root, "SKILL.md")
	if os.IsNotExist(err) {
		return Definition{}, false, "", nil
	}
	if err != nil {
		return Definition{}, false, "", fmt.Errorf("read skill %q: %w", dir, err)
	}
	if len(data) > maxSkillBytes {
		return Definition{}, false, "", fmt.Errorf("skill %q exceeds %d bytes", dir, maxSkillBytes)
	}
	parsed, err := parseSkillMarkdown(data)
	if err != nil {
		return Definition{}, false, "", fmt.Errorf("parse skill %q: %w", dir, err)
	}
	name := parsed.name
	if name == "" {
		name = dir
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\\`) {
		return Definition{}, false, "", fmt.Errorf("skill %q has invalid name", dir)
	}
	// Sanitize every field that reaches the model-facing tool surface.
	name, _ = SanitizeModelFacingText(name, nameMaxLen)
	description, _ := SanitizeModelFacingText(parsed.description, descriptionMaxLen)
	def := Definition{
		Name:             name,
		Origin:           origin,
		Description:      description,
		ShortDescription: sanitizeOptionalText(parsed.shortDescription, shortDescriptionMaxLen),
		ArgsHint:         sanitizeOptionalText(parsed.argsHint, argsHintMaxLen),
		UserInvocable:    parsed.userInvocable,
		Triggers:         sanitizeTriggers(parsed.triggers),
		// Clone preserves nil (omitted) vs non-nil empty (explicit tools: []).
		Tools: slices.Clone(parsed.tools),
	}
	locationInfo, err := root.Lstat(".")
	if err != nil {
		return Definition{}, false, "", fmt.Errorf("read skill %q: %w", dir, err)
	}
	def.location = skillLocation{path: filepath.Clean(sourcePath), info: locationInfo}
	resources, warning := loadDeclaredResources(def.location)
	def.Resources = resources
	def.Instructions = buildPrompt(def, parsed.instructions)
	return def, true, warning, nil
}

// readRegularSkill refuses links and verifies the opened file still matches
// the inspected file. Skill text can be sent to the configured provider, so a
// workspace must not be able to redirect it to arbitrary readable files.
func readRegularSkill(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skill file is not a regular file")
	}
	if !hasSingleLink(info) {
		return nil, fmt.Errorf("skill file has multiple links")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("skill file changed while reading")
	}
	if !hasSingleLink(opened) {
		return nil, fmt.Errorf("skill file links changed while reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSkillBytes+1))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func sanitizeOptionalText(text string, maxLen int) string {
	text, _ = SanitizeModelFacingText(text, maxLen)
	return text
}

// sanitizeTriggers cleans each trigger for the model-facing surface and drops
// entries that sanitize to nothing.
func sanitizeTriggers(raw []string) []string {
	var out []string
	for _, t := range raw {
		t, _ = SanitizeModelFacingText(t, triggerMaxLen)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// buildPrompt renders the model-facing header from the Definition's own fields
// and prepends it to the skill instructions.
func buildPrompt(def Definition, instructions string) string {
	var b strings.Builder
	b.WriteString("Skill: " + def.Name + "\n")
	if def.Description != "" {
		b.WriteString("Description: " + def.Description + "\n")
	}
	if joined := truncateRunes(strings.Join(def.Triggers, "\n"), triggersJoinedMax); joined != "" {
		b.WriteString("Triggers:\n" + joined + "\n")
	}
	if def.Description == "" && len(def.Triggers) == 0 {
		return instructions
	}
	return b.String() + "\n" + instructions
}

// truncateRunes cuts s to at most max bytes without splitting a UTF-8 rune.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Model-facing text caps. These are deliberately chosen starting points,
// not measured limits. If a provider's tool-schema limit is hit in practice,
// re-derive them from that limit rather than tuning by feel.
const (
	nameMaxLen        = 64
	descriptionMaxLen = 200
	triggerMaxLen     = 64
	triggersJoinedMax = 400
)

// knownSkillKeys is the complete recognised frontmatter key set. Anything else
// is rejected, so a field nothing consumes cannot be added silently — the class
// of bug that left `triggers:` inert in nine skills.
var knownSkillKeys = map[string]bool{
	"name": true, "description": true, "triggers": true,
	"user-invocable": true, "argument-hint": true, "short-description": true,
	"tools": true,
}

type parsedSkill struct {
	name, description, argsHint, shortDescription, instructions string
	triggers                                                    []string
	tools                                                       []string
	userInvocable                                               bool
}

func parseSkillMarkdown(data []byte) (parsedSkill, error) {
	normalized := normalizeNewlines(string(data))
	m, closing, err := ParseFrontmatterKnownWithClosing([]byte(normalized), knownSkillKeys)
	if err != nil {
		return parsedSkill{}, err
	}
	var instructions string
	if m == nil {
		instructions = strings.TrimSpace(normalized)
	} else {
		lines := strings.Split(normalized, "\n")
		instructions = strings.TrimSpace(strings.Join(lines[closing+1:], "\n"))
	}
	parsed := parsedSkill{userInvocable: true}
	if m != nil {
		parsed.name, _ = m["name"].(string)
		parsed.description, _ = m["description"].(string)
		switch tv := m["triggers"].(type) {
		case []string:
			parsed.triggers = tv
		case string:
			if tv != "" {
				parsed.triggers = []string{tv}
			}
		}
		parsed.argsHint, _ = m["argument-hint"].(string)
		parsed.shortDescription, _ = m["short-description"].(string)
		if v, ok := m["user-invocable"].(string); ok && v != "" {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true":
				parsed.userInvocable = true
			case "false":
				parsed.userInvocable = false
			default:
				return parsedSkill{}, fmt.Errorf("user-invocable must be true or false")
			}
		}
		tools, err := parseSkillTools(m["tools"])
		if err != nil {
			return parsedSkill{}, err
		}
		parsed.tools = tools
	}
	parsed.instructions = instructions
	return parsed, nil
}

// parseSkillTools coerces frontmatter tools into a non-empty-name string list.
// Omitted key yields nil. Empty list is valid (skill declares no required tools).
func parseSkillTools(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var items []string
	switch v := raw.(type) {
	case []string:
		items = v
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("tools: empty tool name")
		}
		items = []string{v}
	default:
		return nil, fmt.Errorf("tools must be a list of tool names")
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, n := range items {
		n = strings.TrimSpace(n)
		if n == "" {
			return nil, fmt.Errorf("tools: empty tool name")
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out, nil
}
