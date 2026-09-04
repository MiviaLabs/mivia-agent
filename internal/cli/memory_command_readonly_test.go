package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func TestMemorySearchJSONSerializesOrgField(t *testing.T) {
	var out strings.Builder
	results := []memory.Result{{ID: "org-id-1", Scope: memory.ScopeOrg, Org: "github.com/acme", Title: "org note", Verdict: memory.VerdictGood, Tags: []string{"org"}, Created: "2026-08-05", Snippet: "org summary text"}}
	if err := writeMemorySearchJSON(&out, results); err != nil {
		t.Fatal(err)
	}
	var decoded []memorySearchJSONProbe
	if err := json.Unmarshal([]byte(out.String()), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].Org != "github.com/acme" {
		t.Fatalf("decoded = %+v", decoded)
	}
}
