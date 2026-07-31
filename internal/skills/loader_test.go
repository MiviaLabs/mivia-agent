package skills

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLoadMarkdownRegistersCallableInstructionSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Review code\n---\nUse evidence only.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	d := reg.List()[0]
	if d.Name != "review" {
		t.Fatalf("expected name 'review', got %q", d.Name)
	}
	if !strings.Contains(d.Instructions, "Use evidence only") {
		t.Fatalf("instructions missing body: %q", d.Instructions)
	}
}

func TestLoadMarkdownLoadsDeclaredResourcesWithoutReadingTheirBodies(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: review\n---\nUse the fallback when needed."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resources.toml"), []byte("format = 1\n\n[[resources]]\nid = \"fallback-report\"\npath = \"report-template.md\"\nsummary = \"Generic report structure\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report-template.md"), []byte("PRIVATE TEMPLATE BODY"), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("review")
	if !ok {
		t.Fatal("skill missing")
	}
	if len(def.Resources) != 1 || def.Resources[0].ID != "fallback-report" || def.Resources[0].Summary != "Generic report structure" {
		t.Fatalf("resources = %#v", def.Resources)
	}
	if strings.Contains(def.Instructions, "PRIVATE TEMPLATE BODY") {
		t.Fatal("resource body was read during discovery")
	}
}

func TestActivationReadsOnlyDeclaredResourceAndCachesItsFirstValue(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"SKILL.md":           "---\nname: review\n---\nReview.",
		"resources.toml":     "format = 1\n\n[[resources]]\nid = \"fallback-report\"\npath = \"report-template.md\"\nsummary = \"Generic report structure\"\n",
		"report-template.md": "template one",
		"undeclared.md":      "must never be readable",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("review")
	activation, err := def.Activate()
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Close()
	first, err := activation.Read(context.Background(), "fallback-report")
	if err != nil || first.Text != "template one" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report-template.md"), []byte("template two"), 0o600); err != nil {
		t.Fatal(err)
	}
	cached, err := activation.Read(context.Background(), "fallback-report")
	if err != nil || cached.Text != "template one" {
		t.Fatalf("cached=%+v err=%v", cached, err)
	}
	if _, err := activation.Read(context.Background(), "undeclared"); err == nil || strings.Contains(err.Error(), "undeclared.md") {
		t.Fatalf("undeclared read error=%v", err)
	}
}

func TestLoadMarkdownSourcesKeepsProjectOverrideWhenItsManifestIsMalformed(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	for root, source := range map[string]struct{ body, manifest string }{
		user:    {"---\nname: review\n---\nuser", "format = 1\n\n[[resources]]\nid = \"user-template\"\npath = \"user.md\"\nsummary = \"User template\"\n"},
		project: {"---\nname: review\n---\nproject", "format = 2"},
	} {
		dir := filepath.Join(root, "review")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(source.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "resources.toml"), []byte(source.manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, warnings, err := LoadMarkdownSources([]Source{{Dir: user, Origin: OriginUser}, {Dir: project, Origin: OriginProject}}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("review")
	if !ok || def.Origin != OriginProject || !strings.Contains(def.Instructions, "project") || len(def.Resources) != 0 {
		t.Fatalf("override definition=%#v", def)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "ignore invalid skill resources") {
		t.Fatalf("warnings=%v", warnings)
	}
}

func TestActivationRejectsReplacedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"SKILL.md":       "---\nname: review\n---\nbody",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Template\"\n",
		"template.md":    "safe",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("review")
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := def.Activate(); err == nil {
		t.Fatal("replaced skill directory was accepted")
	}
}

func TestActivationRejectsSymlinkedResourcePath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"SKILL.md":       "---\nname: review\n---\nbody",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"references/template.md\"\nsummary = \"Template\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "outside.md")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "references", "template.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("review")
	activation, err := def.Activate()
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Close()
	if _, err := activation.Read(context.Background(), "template"); err == nil {
		t.Fatal("symlinked resource was accepted")
	}
}

func TestLoadMarkdownMissingDirectoryIsEmpty(t *testing.T) {
	reg, err := loadMarkdown(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(reg.List()) != 0 {
		t.Fatalf("registry=%v err=%v", reg, err)
	}
}

func TestParseMarkdownRequiresCompleteClosingDelimiter(t *testing.T) {
	parsed, err := parseSkillMarkdown([]byte("---\nname: x\n---\n---example\nkeep"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.instructions, "---example") || !strings.Contains(parsed.instructions, "keep") {
		t.Fatalf("instructions=%q", parsed.instructions)
	}
}

func TestParseMarkdownDoesNotTreatPrefixAsFrontmatter(t *testing.T) {
	parsed, err := parseSkillMarkdown([]byte("---example\nkeep"))
	if err != nil || parsed.instructions != "---example\nkeep" {
		t.Fatalf("instructions=%q err=%v", parsed.instructions, err)
	}
}

func TestParseMarkdownExtractsTriggers(t *testing.T) {
	parsed, err := parseSkillMarkdown([]byte("---\nname: review\ndescription: Review code\ntriggers:\n  - architecture review\n  - design review\n---\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.triggers) != 2 || parsed.triggers[0] != "architecture review" || parsed.triggers[1] != "design review" {
		t.Fatalf("triggers = %v", parsed.triggers)
	}
}

func TestLoadMarkdownInjectsTriggersIntoPrompt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Review code\ntriggers:\n  - arch review\n  - design review\n---\nUse evidence only.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	d := reg.List()[0]
	if len(d.Triggers) != 2 {
		t.Fatalf("expected 2 triggers, got %v", d.Triggers)
	}
	// The prompt should contain "Triggers:" followed by the trigger phrases.
	if !strings.Contains(d.Instructions, "Triggers:") {
		t.Fatalf("prompt missing Triggers section: %q", d.Instructions)
	}
	if !strings.Contains(d.Instructions, "arch review") {
		t.Fatalf("prompt missing trigger 'arch review': %q", d.Instructions)
	}
}

func TestLoadMarkdownHandlesSkillWithoutTriggers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "simple")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: simple\ndescription: Simple skill\n---\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	d := reg.List()[0]
	if len(d.Triggers) != 0 {
		t.Fatalf("expected 0 triggers, got %v", d.Triggers)
	}
	// Prompt should not contain "Triggers:".
	if strings.Contains(d.Instructions, "Triggers:") {
		t.Fatalf("prompt should not contain Triggers: %q", d.Instructions)
	}
}

func TestLoadMarkdownSourcesMergesScopesAndSkillMetadata(t *testing.T) {
	project := t.TempDir()
	user := t.TempDir()
	for _, tc := range []struct {
		root, dir, body string
	}{
		{user, "review", "---\nname: review\ndescription: user description\nuser-invocable: false\nargument-hint: <path>\nshort-description: User review\n---\nuser body"},
		{project, "review", "---\nname: review\ndescription: project description\nargument-hint: <scope>\nshort-description: Project review\n---\nproject body"},
		{user, "bad", "---\nname: bad\nunknown: nope\n---\nbody"},
		{user, "multi", "---\nname: multi_step\ndescription: reserved\n---\nbody"},
	} {
		dir := filepath.Join(tc.root, tc.dir)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reg, warnings, err := LoadMarkdownSources([]Source{
		{Dir: user, Origin: OriginUser},
		{Dir: project, Origin: OriginProject},
	}, LoadOptions{ReservedNames: map[string]struct{}{"multi_step": {}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v, want malformed, reserved, and project-shadow notices", warnings)
	}
	d, ok := reg.Get("review")
	if !ok {
		t.Fatal("merged project skill missing")
	}
	if d.Origin != OriginProject || d.Description != "project description" || !d.UserInvocable || d.ArgsHint != "<scope>" || d.ShortDescription != "Project review" {
		t.Fatalf("merged definition = %#v", d)
	}
	if _, ok := reg.Get("bad"); ok {
		t.Fatal("malformed skill must be skipped")
	}
	if _, ok := reg.Get("multi_step"); ok {
		t.Fatal("reserved skill must be skipped")
	}
}

func TestSanitizeModelFacingTextKeepsUTF8AtBoundary(t *testing.T) {
	got, truncated := SanitizeModelFacingText(strings.Repeat("界", 30), 65)
	if !truncated || !utf8.ValidString(got) || len(got) > 65 {
		t.Fatalf("sanitized = %q truncated=%v bytes=%d valid=%v", got, truncated, len(got), utf8.ValidString(got))
	}
}

func TestLoadMarkdownSourcesWarnsForSlashEligibility(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ dir, name string }{{"unicode", "résumé"}, {"builtin", "help"}} {
		dir := filepath.Join(root, tc.dir)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + tc.name + "\ndescription: test\n---\nbody"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, warnings, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{ReservedSlashTokens: map[string]struct{}{"/help": {}}})
	if err != nil || len(reg.List()) != 2 {
		t.Fatalf("registry=%v warnings=%v err=%v", reg, warnings, err)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings=%v, want unsluggable and builtin collision", warnings)
	}
}

func TestLoadMarkdownSourcesWarningsNeverEchoSkillNames(t *testing.T) {
	root := t.TempDir()
	name := "alice@example.com"
	dir := filepath.Join(root, "private")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test\n---\nbody"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{})
	if err != nil || len(warnings) != 1 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	if strings.Contains(strings.Join(warnings, "\n"), name) {
		t.Fatalf("warning leaked skill name: %v", warnings)
	}
}

func TestLoadMarkdownRejectsSymlinkedSkillFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "linked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.md")
	if err := os.WriteFile(target, []byte("private instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("symlinked skill file was accepted")
	}
	reg, warnings, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{})
	if err != nil || len(reg.List()) != 0 || len(warnings) != 1 {
		t.Fatalf("sources registry=%v warnings=%v err=%v", reg, warnings, err)
	}
}

func TestLoadMarkdownRejectsSymlinkedSkillsRoot(t *testing.T) {
	parent := t.TempDir()
	target := t.TempDir()
	dir := filepath.Join(target, "leak")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("private instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "skills")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("symlinked skills root was accepted")
	}
	reg, warnings, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{})
	if err != nil || len(reg.List()) != 0 || len(warnings) != 1 {
		t.Fatalf("sources registry=%v warnings=%v err=%v", reg, warnings, err)
	}
}

func TestLoadMarkdownRejectsHardLinkedSkillFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "linked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(target, []byte("private instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("hard-linked skill file was accepted")
	}
}

func TestPinnedSkillRootRejectsDirectorySwapRedirect(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "skills")
	dir := filepath.Join(root, "review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("safe instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinned, err := openSkillRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	external := t.TempDir()
	if err := os.Mkdir(filepath.Join(external, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "review", "SKILL.md"), []byte("private instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	entries, err := fs.ReadDir(pinned.FS(), ".")
	if err != nil || len(entries) != 1 {
		t.Fatalf("pinned entries=%v err=%v", entries, err)
	}
	skillDir, ok, err := openSkillDirectory(pinned, "review")
	if err != nil || !ok {
		t.Fatalf("pinned skill directory ok=%v err=%v", ok, err)
	}
	defer skillDir.Close()
	data, err := readRegularSkill(skillDir, "SKILL.md")
	if err != nil || string(data) != "safe instructions" {
		t.Fatalf("pinned skill data=%q err=%v", data, err)
	}
}

func TestLoadMarkdownRejectsMalformedAndOversizedSkills(t *testing.T) {
	root := t.TempDir()
	badDir := filepath.Join(root, "bad")
	if err := os.Mkdir(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("---\nname: bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("malformed frontmatter accepted")
	}
	root = t.TempDir()
	largeDir := filepath.Join(root, "large")
	if err := os.Mkdir(largeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(largeDir, "SKILL.md"), make([]byte, maxSkillBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("oversized skill accepted")
	}
}

// writeSkill is a helper for the audit-regression tests below.
func writeSkill(t *testing.T, name, content string) *Registry {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return reg
}

// Pins INV-AG-17: an unrecognised frontmatter key is rejected at load, not
// silently discarded. Regression for a dead-field bug shipped as a dead check.
func TestLoadMarkdownRejectsUnknownFrontmatterKey(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\nbogus_key: whatever\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(root, "x", "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadMarkdown(root)
	if err == nil {
		t.Fatal("expected unknown frontmatter key to be rejected")
	}
	if !strings.Contains(err.Error(), "bogus_key") {
		t.Fatalf("error must name the offending key, got %v", err)
	}
}

// The model-facing prompt is rendered from Definition.Triggers, so the field
// has a production reader rather than being written and never read.
func TestPromptRendersFromDefinitionTriggers(t *testing.T) {
	def := Definition{Name: "n", Description: "d", Triggers: []string{"alpha", "beta"}}
	got := buildPrompt(def, "body")
	for _, want := range []string{"Triggers:", "alpha", "beta"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt %q missing %q", got, want)
		}
	}
}

// Joined triggers are cut on a rune boundary; a byte slice would leave a
// dangling continuation byte in model-facing text.
func TestTruncateRunesNeverSplitsARune(t *testing.T) {
	s := strings.Repeat("→", 140) // 3-byte runes, 420 bytes
	got := truncateRunes(s, triggersJoinedMax)
	if len(got) > triggersJoinedMax {
		t.Fatalf("truncate exceeded cap: %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a UTF-8 rune")
	}
}

// TestSingleParseSkillMarkdown verifies that parseSkillMarkdown extracts all
// known keys from a single parse call and produces correct instructions.
func TestSingleParseSkillMarkdown(t *testing.T) {
	input := []byte("---\nname: review\ndescription: Review code\ntriggers:\n  - arch review\n  - design review\nargument-hint: <path>\nshort-description: Quick review\nuser-invocable: false\ntools:\n  - read_file\n  - grep\n---\nReview instructions here.\n")
	parsed, err := parseSkillMarkdown(input)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.name != "review" {
		t.Fatalf("name=%q", parsed.name)
	}
	if parsed.description != "Review code" {
		t.Fatalf("description=%q", parsed.description)
	}
	if len(parsed.triggers) != 2 || parsed.triggers[0] != "arch review" || parsed.triggers[1] != "design review" {
		t.Fatalf("triggers=%v", parsed.triggers)
	}
	if parsed.argsHint != "<path>" {
		t.Fatalf("argsHint=%q", parsed.argsHint)
	}
	if parsed.shortDescription != "Quick review" {
		t.Fatalf("shortDescription=%q", parsed.shortDescription)
	}
	if parsed.userInvocable {
		t.Fatal("userInvocable should be false")
	}
	if len(parsed.tools) != 2 || parsed.tools[0] != "read_file" || parsed.tools[1] != "grep" {
		t.Fatalf("tools=%v", parsed.tools)
	}
	if !strings.Contains(parsed.instructions, "Review instructions here") {
		t.Fatalf("instructions=%q", parsed.instructions)
	}
}

// TestSkillToolsParsedAndPublished proves frontmatter tools reach Definition.Tools
// with non-empty values when the fixture declares them (plan 06 phase 01).
func TestSkillToolsParsedAndPublished(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "audit")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: audit\ndescription: Audit\ntools:\n  - read_file\n  - grep\n  - run_command\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("audit")
	if !ok {
		t.Fatal("skill missing")
	}
	if len(def.Tools) == 0 {
		t.Fatal("Definition.Tools must be non-empty when fixture declares tools")
	}
	want := []string{"read_file", "grep", "run_command"}
	if len(def.Tools) != len(want) {
		t.Fatalf("Tools=%v want %v", def.Tools, want)
	}
	for i, n := range want {
		if def.Tools[i] != n {
			t.Fatalf("Tools[%d]=%q want %q", i, def.Tools[i], n)
		}
	}
	if def.Origin != OriginProject {
		t.Fatalf("origin=%q", def.Origin)
	}
}

func TestSkillToolsFlowSequence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools: [read_file, write_file]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("x")
	if len(def.Tools) != 2 || def.Tools[0] != "read_file" || def.Tools[1] != "write_file" {
		t.Fatalf("Tools=%v", def.Tools)
	}
}

func TestSkillToolsScalarAcceptedAsSingleton(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools: read_file\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("x")
	if len(def.Tools) != 1 || def.Tools[0] != "read_file" {
		t.Fatalf("Tools=%v", def.Tools)
	}
}

func TestSkillToolsMalformedRejected(t *testing.T) {
	// Empty list item is rejected by the frontmatter parser.
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools:\n  - \n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("malformed tools list must be rejected")
	}
}

func TestSkillToolsEmptyNameRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Quoted empty string survives frontmatter as "" — parser must reject it.
	content := "---\nname: x\ntools: [\"\"]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("empty tool name must be rejected")
	}
}

func TestSkillToolsOmittedIsNil(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("x")
	if def.Tools != nil {
		t.Fatalf("omitted tools must stay nil, got %v", def.Tools)
	}
}
