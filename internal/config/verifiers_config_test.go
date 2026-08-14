package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVerifiersLayerValid(t *testing.T) {
	data := []byte(`
[verifiers.go-test]
go_module_baseline = true
commands = [ { check = "go-test", program = "go", args = ["test", "./..."] } ]

[verifiers.lint]
commands = [
  { check = "vet", program = "go", args = ["vet", "./..."] },
  { check = "build", program = "go", args = ["build", "./..."] },
]
`)
	profiles, err := parseVerifiersLayer(data, "test.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	goTest, ok := profiles["go-test"]
	if !ok || !goTest.GoModuleBaseline || len(goTest.Commands) != 1 {
		t.Fatalf("go-test profile is wrong: %+v", goTest)
	}
	if goTest.Commands[0].Program != "go" || goTest.Commands[0].Args[0] != "test" {
		t.Fatalf("go-test command is wrong: %+v", goTest.Commands[0])
	}
	lint := profiles["lint"]
	if lint.GoModuleBaseline || len(lint.Commands) != 2 {
		t.Fatalf("lint profile is wrong: %+v", lint)
	}
}

func TestParseVerifiersLayerRejections(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want string
	}{
		{"unknown table key", "[verifiers.a]\ncommand = []\n", "unknown key \"command\""},
		{"unknown command key", "[verifiers.a]\ncommands = [ { check = \"c\", program = \"go\", shell = \"x\" } ]\n", "unknown key \"shell\""},
		{"empty commands", "[verifiers.a]\ncommands = []\n", "non-empty"},
		{"missing commands", "[verifiers.a]\ngo_module_baseline = true\n", "non-empty"},
		{"path program", "[verifiers.a]\ncommands = [ { check = \"c\", program = \"./run.sh\" } ]\n", "bare executable"},
		{"bad profile name", "[verifiers.\"Bad_Name\"]\ncommands = [ { check = \"c\", program = \"go\" } ]\n", "lowercase alphanumeric"},
		{"empty check", "[verifiers.a]\ncommands = [ { check = \"\", program = \"go\" } ]\n", "check is required"},
		{"non-string args", "[verifiers.a]\ncommands = [ { check = \"c\", program = \"go\", args = [1] } ]\n", "array of strings"},
		{"non-bool flag", "[verifiers.a]\ngo_module_baseline = \"yes\"\ncommands = [ { check = \"c\", program = \"go\" } ]\n", "must be a boolean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseVerifiersLayer([]byte(tc.toml), "test.toml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// A later layer must replace a profile wholesale: the overlay's go-test below
// omits go_module_baseline, and the merged result must not keep the base
// layer's flag or commands.
func TestVerifierLayerOverlayWinsWholeProfile(t *testing.T) {
	var file File
	base := []byte(`
[verifiers.go-test]
go_module_baseline = true
commands = [ { check = "base", program = "go", args = ["test"] } ]

[verifiers.keep]
commands = [ { check = "keep", program = "go" } ]
`)
	overlay := []byte(`
[verifiers.go-test]
commands = [ { check = "overlay", program = "make" } ]
`)
	for _, layer := range [][]byte{base, overlay} {
		profiles, err := parseVerifiersLayer(layer, "layer.toml")
		if err != nil {
			t.Fatalf("parse layer: %v", err)
		}
		file.Verifiers = mergeVerifierLayer(file.Verifiers, profiles)
	}
	got := file.Verifiers["go-test"]
	if got.GoModuleBaseline {
		t.Fatalf("overlay must clear the base layer's go_module_baseline")
	}
	if len(got.Commands) != 1 || got.Commands[0].Check != "overlay" || got.Commands[0].Program != "make" {
		t.Fatalf("overlay must replace commands wholesale: %+v", got.Commands)
	}
	if _, ok := file.Verifiers["keep"]; !ok {
		t.Fatalf("profiles the overlay does not name must survive")
	}
}

func TestLoadWorkspaceVerifiers(t *testing.T) {
	root := t.TempDir()
	if profiles, err := LoadWorkspaceVerifiers(root); err != nil || profiles != nil {
		t.Fatalf("missing config must mean no profiles, got %v, %v", profiles, err)
	}
	dir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "[verifiers.checks]\ncommands = [ { check = \"unit\", program = \"go\", args = [\"test\", \"./...\"] } ]\n"
	if err := os.WriteFile(filepath.Join(dir, "mivia.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadWorkspaceVerifiers(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := profiles["checks"]; !ok {
		t.Fatalf("declared profile is missing: %+v", profiles)
	}
}
