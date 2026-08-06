package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestParseResourceManifestRejectsUnsafeDeclarations(t *testing.T) {
	for name, manifest := range map[string]string{
		"unknown key":    "format = 1\nother = true\n",
		"duplicate ID":   "format = 1\n[[resources]]\nid = \"same\"\npath = \"one.md\"\nsummary = \"One\"\n[[resources]]\nid = \"same\"\npath = \"two.md\"\nsummary = \"Two\"\n",
		"duplicate path": "format = 1\n[[resources]]\nid = \"one\"\npath = \"same.md\"\nsummary = \"One\"\n[[resources]]\nid = \"two\"\npath = \"same.md\"\nsummary = \"Two\"\n",
		"traversal path": "format = 1\n[[resources]]\nid = \"one\"\npath = \"../secret.md\"\nsummary = \"One\"\n",
		"invalid ID":     "format = 1\n[[resources]]\nid = \"Not-safe\"\npath = \"one.md\"\nsummary = \"One\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseResourceManifest([]byte(manifest)); err == nil {
				t.Fatal("unsafe manifest was accepted")
			}
		})
	}
}

func TestDefinitionSnapshotResourcesUsesOnlySafeResourceFields(t *testing.T) {
	definition := loadResourceTestDefinition(t, []byte("resource body"))
	snapshots, err := definition.SnapshotResources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d", len(snapshots))
	}
	wantDigest := sha256.Sum256([]byte("resource body"))
	got := snapshots[0]
	if got.ID != "template" || got.Text != "resource body" || got.Digest != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestLoadMarkdownSourcesDoesNotWarnWhenResourceManifestIsAbsent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: review\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, warnings, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.List()) != 1 || len(warnings) != 0 {
		t.Fatalf("skills=%v warnings=%v", registry.List(), warnings)
	}
}

func TestActivationRejectsBinaryAndOversizedResources(t *testing.T) {
	for name, body := range map[string][]byte{
		"binary":    []byte("not text\x00"),
		"oversized": make([]byte, maxResourceBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			definition := loadResourceTestDefinition(t, body)
			activation, err := definition.Activate()
			if err != nil {
				t.Fatal(err)
			}
			defer activation.Close()
			if _, err := activation.Read(context.Background(), "template"); err == nil {
				t.Fatal("unsafe resource was accepted")
			}
		})
	}
}

func TestActivationRejectsHardLinkedResource(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: review\n---\nbody",
		"resources.toml": "format = 1\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Template\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "outside.md")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, "template.md")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	registry, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Get("review")
	activation, err := definition.Activate()
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Close()
	if _, err := activation.Read(context.Background(), "template"); err == nil {
		t.Fatal("hard-linked resource was accepted")
	}
}

func TestProjectOverrideBindsItsOwnResource(t *testing.T) {
	userRoot, projectRoot := t.TempDir(), t.TempDir()
	for root, text := range map[string]string{userRoot: "USER TEMPLATE", projectRoot: "PROJECT TEMPLATE"} {
		dir := filepath.Join(root, "review")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			"SKILL.md":       "---\nname: review\n---\nbody",
			"resources.toml": "format = 1\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Template\"\n",
			"template.md":    text,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, _, err := LoadMarkdownSources([]Source{
		{Dir: userRoot, Origin: OriginUser},
		{Dir: projectRoot, Origin: OriginProject},
	}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Get("review")
	if definition.Origin != OriginProject {
		t.Fatalf("origin=%q", definition.Origin)
	}
	activation, err := definition.Activate()
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Close()
	resource, err := activation.Read(context.Background(), "template")
	if err != nil || resource.Text != "PROJECT TEMPLATE" {
		t.Fatalf("resource=%+v err=%v", resource, err)
	}
}

func TestActivationPinsResourceDirectoryAcrossReplacement(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: review\n---\nbody",
		"resources.toml": "format = 1\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Template\"\n",
		"template.md":    "ORIGINAL TEMPLATE",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Get("review")
	activation, err := definition.Activate()
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Close()
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "template.md"), []byte("REPLACEMENT TEMPLATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	resource, err := activation.Read(context.Background(), "template")
	if err != nil || resource.Text != "ORIGINAL TEMPLATE" {
		t.Fatalf("resource=%+v err=%v", resource, err)
	}
}

func loadResourceTestDefinition(t *testing.T, body []byte) Definition {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		"SKILL.md":       []byte("---\nname: review\n---\nbody"),
		"resources.toml": []byte("format = 1\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Template\"\n"),
		"template.md":    body,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.Get("review")
	if !ok {
		t.Fatal("resource skill missing")
	}
	return definition
}
