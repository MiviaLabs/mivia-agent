package tools

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// TestMemorySaveCannotSetTier is a structural proxy for D1a's claim that no
// code path reachable from memory_save can set or change an entry's tier
// (true whole-program reachability has no Go unit-test primitive; the actual
// reachability evidence is a Step 1 t2e review finding, not this test).
//
// memory.Entry is the only value memorySaveTool.Execute passes to
// Store.Save (see memory.go), and Save never names the tier column in its
// INSERT - every new row lands at the schema default ("archive"). This test
// confirms the structural half of that guarantee: Entry has no field a
// caller could use to express a tier, so the tool's JSON-decoded input can
// never carry one either.
func TestMemorySaveCannotSetTier(t *testing.T) {
	typ := reflect.TypeOf(memory.Entry{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if strings.EqualFold(name, "tier") {
			t.Fatalf("memory.Entry has a %q field; memory_save could set tier, breaking D1a's promotion gate", name)
		}
	}
}
