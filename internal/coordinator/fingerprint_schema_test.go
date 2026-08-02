package coordinator

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestFingerprintDiffersWithOutputSchema(t *testing.T) {
	base := subagents.Task{ID: "t1", Name: "worker", Input: []byte(`"hi"`)}
	withSchema := base
	withSchema.OutputSchema = map[string]any{"type": "object"}

	a, err := requestFingerprint([]subagents.Task{base})
	if err != nil {
		t.Fatal(err)
	}
	b, err := requestFingerprint([]subagents.Task{withSchema})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("fingerprint must change when OutputSchema is set")
	}
}

// fingerprintExcludedTaskFields are Task fields that are deliberately not
// part of the work fingerprint (caller identity, coordination keys).
var fingerprintExcludedTaskFields = map[string]bool{
	"Owner": true, "SessionID": true, "TurnID": true, "Role": true,
	"InvocationKey": true, "Depth": true, "IdempotencyKey": true,
}

func TestFingerprintCoversWorkDefiningTaskFields(t *testing.T) {
	fpType := reflect.TypeOf(fingerprintTask{})
	fpFields := map[string]bool{}
	for i := 0; i < fpType.NumField(); i++ {
		fpFields[fpType.Field(i).Name] = true
	}
	taskType := reflect.TypeOf(subagents.Task{})
	for i := 0; i < taskType.NumField(); i++ {
		name := taskType.Field(i).Name
		if fingerprintExcludedTaskFields[name] {
			continue
		}
		if !fpFields[name] {
			t.Errorf("Task field %q is work-defining but missing from fingerprintTask (add it or list it in fingerprintExcludedTaskFields)", name)
		}
	}
}
