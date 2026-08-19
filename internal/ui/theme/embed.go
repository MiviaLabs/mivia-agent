package theme

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
)

// themeFS embeds the shipped theme definitions. Adding a theme is a data
// change here, never a code change.
//
//go:embed themes/*.json
var themeFS embed.FS

// Embedded loads every theme shipped in themes/*.json, sorted by name.
func Embedded() ([]Theme, error) {
	entries, err := fs.Glob(themeFS, "themes/*.json")
	if err != nil {
		return nil, fmt.Errorf("theme: glob embedded themes: %w", err)
	}
	sort.Strings(entries)

	themes := make([]Theme, 0, len(entries))
	for _, name := range entries {
		raw, err := fs.ReadFile(themeFS, name)
		if err != nil {
			return nil, fmt.Errorf("theme: read %s: %w", name, err)
		}
		var t Theme
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("theme: parse %s: %w", name, err)
		}
		if t.Name == "" {
			return nil, fmt.Errorf("theme: %s missing \"name\"", name)
		}
		themes = append(themes, t)
	}
	return themes, nil
}

// LoadUserDir loads user-supplied theme JSON files from a config
// directory. A missing directory is not an error: it means no user
// themes are installed.
func LoadUserDir(dir string) ([]Theme, error) {
	entries, err := fs.Glob(os.DirFS(dir), "*.json")
	if err != nil {
		if _, statErr := fs.Stat(os.DirFS(dir), "."); statErr != nil {
			return nil, nil
		}
		return nil, fmt.Errorf("theme: glob user themes in %s: %w", dir, err)
	}
	themes := make([]Theme, 0, len(entries))
	for _, name := range entries {
		raw, err := fs.ReadFile(os.DirFS(dir), name)
		if err != nil {
			return nil, fmt.Errorf("theme: read %s: %w", path.Join(dir, name), err)
		}
		var t Theme
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("theme: parse %s: %w", path.Join(dir, name), err)
		}
		t.FirstParty = false
		themes = append(themes, t)
	}
	return themes, nil
}
