package definition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vocabWorkflow(transitionStatus, inputType string) string {
	return "version = 1\nname = \"vocab\"\ninitial_step = \"plan\"\n\n" +
		"[inputs]\ntask = { type = \"" + inputType + "\" }\n\n" +
		"[[steps]]\nid = \"plan\"\nkind = \"human_gate\"\n\n" +
		"[[transitions]]\nfrom = \"plan\"\nto = \"success\"\nmatch = { status = \"" + transitionStatus + "\" }\n"
}

// TestTransitionStatusVocabularyIsEnforced closes a silent dead-config hole.
// The runtime routes only "succeeded" and "failed"; any other status is an
// edge that can never fire. Unchecked, the natural typo status = "success"
// compiled clean and then routed the final step's success to zero_match, so
// the run failed having done all of its work.
func TestTransitionStatusVocabularyIsEnforced(t *testing.T) {
	for _, status := range []string{"success", "approved", "completed", "passed", "SUCCEEDED"} {
		_, _, err := ParseWorkflowTOML([]byte(vocabWorkflow(status, "string")), "vocab.toml")
		if err == nil {
			t.Errorf("match.status %q compiled clean; it can never fire at runtime", status)
			continue
		}
		if !strings.Contains(err.Error(), "match.status") {
			t.Errorf("status %q: error %q should name match.status", status, err)
		}
	}
	for _, status := range []string{"succeeded", "failed"} {
		if _, _, err := ParseWorkflowTOML([]byte(vocabWorkflow(status, "string")), "vocab.toml"); err != nil {
			t.Errorf("match.status %q was rejected but the runtime does emit it: %v", status, err)
		}
	}
}

// TestInputTypeVocabularyIsEnforced closes the matching hole for input types.
// ParseInputValue supports exactly six, and errors on anything else - but only
// once a value is supplied. So `type = "int"` shipped silently and then failed
// EVERY run at admission for a required input.
func TestInputTypeVocabularyIsEnforced(t *testing.T) {
	for _, typ := range []string{"int", "bool", "float", "str", "String"} {
		_, _, err := ParseWorkflowTOML([]byte(vocabWorkflow("succeeded", typ)), "vocab.toml")
		if err == nil {
			t.Errorf("input type %q compiled clean; admission would reject every run", typ)
			continue
		}
		if !strings.Contains(err.Error(), "type") {
			t.Errorf("type %q: error %q should name the type", typ, err)
		}
	}
	for typ := range ValidInputTypes {
		if _, _, err := ParseWorkflowTOML([]byte(vocabWorkflow("succeeded", typ)), "vocab.toml"); err != nil {
			t.Errorf("input type %q was rejected but ParseInputValue supports it: %v", typ, err)
		}
	}
}

// TestValidInputTypesMatchesParseInputValue keeps the declared vocabulary and
// the decoder from drifting apart: every type the list admits must decode, and
// a type it omits must not.
func TestValidInputTypesMatchesParseInputValue(t *testing.T) {
	samples := map[string]string{
		"string":  "anything",
		"boolean": "true",
		"integer": "7",
		"number":  "1.5",
		"object":  `{"a":"b"}`,
		"array":   `["a"]`,
	}
	for typ := range ValidInputTypes {
		sample, ok := samples[typ]
		if !ok {
			t.Fatalf("ValidInputTypes contains %q with no sample here; add one so the two stay in step", typ)
		}
		if _, err := ParseInputValue(sample, typ); err != nil {
			t.Errorf("ParseInputValue(%q, %q) = %v, but the type is declared valid", sample, typ, err)
		}
	}
	if _, err := ParseInputValue("1", "int"); err == nil {
		t.Error("ParseInputValue accepted the undeclared type \"int\"")
	}
}

// TestShippedWorkflowsCompile is the gap the audit named: four of the six
// definitions this repo ships were compiled by NO test, so a semantic break in
// one surfaced only when a human ran it. This walks every shipped file through
// the same pipeline the CLI uses.
func TestShippedWorkflowsCompile(t *testing.T) {
	paths, err := filepath.Glob("../../../.mivia/workflows/*.toml")
	if err != nil {
		t.Fatalf("glob shipped workflows: %v", err)
	}
	if len(paths) < 6 {
		t.Fatalf("found %d shipped workflow definitions, want at least 6; the glob is wrong", len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			wf, _, err := ParseWorkflowTOML(raw, filepath.Base(path))
			if err != nil {
				t.Fatalf("ParseWorkflowTOML: %v", err)
			}
			compiled, err := Compile(&wf)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if compiled.InitialStep == "" {
				t.Error("compiled workflow has no initial step")
			}
			if len(compiled.Steps) == 0 {
				t.Error("compiled workflow has no steps")
			}
		})
	}
}
