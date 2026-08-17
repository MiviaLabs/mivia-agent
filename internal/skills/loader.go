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

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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

// validateDeclaredSkillTools rejects any declared tool name that is not in the
// static declared-tool catalogue (plan 43). Omitted (nil) and explicit empty
// tool lists pass; unknown names fail closed.
func validateDeclaredSkillTools(names []string) error {
	for _, name := range names {
		if !tools.IsDeclaredToolName(name) {
			return fmt.Errorf("declares unknown tool %q", name)
		}
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
	// Sanitize before validating: SanitizeModelFacingText strips characters
	// (control bytes, backslash, double quote) that survive TrimSpace and the
	// empty / `\` check below. A check on the raw value would let a name such
	// as a raw control byte pass and then sanitize to "", registering a
	// degenerate skill under the empty name in the resilient loader, which
	// writes registry.items directly (bypassing Registry.Register's
	// empty-name guard) and would surface an empty enum/name on the
	// model-facing tool surface.
	name, _ = SanitizeModelFacingText(name, nameMaxLen)
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\\`) {
		return Definition{}, false, "", fmt.Errorf("skill %q has invalid name", dir)
	}
	// Sanitize every remaining field that reaches the model-facing tool surface.
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
	// Admit schemas at load time (fail closed for uncompilable / remote $ref).
	// Resilient multi-source loader turns this into a skip+warning.
	if len(parsed.outputSchema) > 0 {
		if _, err := jschema.Compile(parsed.outputSchema); err != nil {
			return Definition{}, false, "", fmt.Errorf("skill %q output_schema: %w", dir, err)
		}
		def.OutputSchema = parsed.outputSchema
	}
	if len(parsed.inputSchema) > 0 {
		if _, err := jschema.Compile(parsed.inputSchema); err != nil {
			return Definition{}, false, "", fmt.Errorf("skill %q input_schema: %w", dir, err)
		}
		def.InputSchema = parsed.inputSchema
	}
	locationInfo, err := root.Lstat(".")
	if err != nil {
		return Definition{}, false, "", fmt.Errorf("read skill %q: %w", dir, err)
	}
	def.location = skillLocation{path: filepath.Clean(sourcePath), info: locationInfo}
	// Plan 43: every statically declared tool must be in the declared-tool
	// catalogue. Unknown names (including the activation-only
	// read_skill_resource) fail closed. The strict single-source loader
	// propagates the error; the resilient multi-source loader skips the
	// offending skill with a bounded warning.
	if err := validateDeclaredSkillTools(def.Tools); err != nil {
		return Definition{}, false, "", fmt.Errorf("skill %q: %w", dir, err)
	}
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
