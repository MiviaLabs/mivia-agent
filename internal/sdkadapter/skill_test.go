package sdkadapter

import (
	"reflect"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/skills"
)

// TestSkillSDKBridgeRoundTrip pins the four-field round-trip: an SDK
// Skill with Name, Instructions, Triggers, and RequiredTools converts
// to a CLI Definition carrying the same Name and Instructions, and the
// triggers/tools slices land on the CLI's Triggers and Tools fields.
func TestSkillSDKBridgeRoundTrip(t *testing.T) {
	in := sdkshape.Skill{
		Name:          "demo",
		Instructions:  "demo instructions",
		Triggers:      []string{"go", "build"},
		RequiredTools: []string{"run_command", "read"},
	}
	cli := SDKSkillToCLI(in)
	if cli.Name != in.Name {
		t.Fatalf("Name mismatch: %q vs %q", cli.Name, in.Name)
	}
	if cli.Instructions != in.Instructions {
		t.Fatalf("Instructions mismatch")
	}
	if !reflect.DeepEqual(cli.Triggers, in.Triggers) {
		t.Fatalf("Triggers mismatch: %v vs %v", cli.Triggers, in.Triggers)
	}
	if !reflect.DeepEqual(cli.Tools, in.RequiredTools) {
		t.Fatalf("Tools mismatch: %v vs %v", cli.Tools, in.RequiredTools)
	}
	out := CLISkillToSDK(cli)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", out, in)
	}
}

// TestSkillCLIToSDKProductFieldsZero pins the 13 product-layer fields
// the SDK cannot represent. Every one must remain zero on the SDK
// output; otherwise a CLI Definition that depended on one of those
// fields would silently lose data on the bridge.
func TestSkillCLIToSDKProductFieldsZero(t *testing.T) {
	in := skills.Definition{
		Name:             "demo",
		Version:          "1.2.3",
		Scope:            "scope",
		Origin:           skills.OriginProject,
		Permission:       "ask",
		Description:      "describes the skill",
		ShortDescription: "short",
		ArgsHint:         "filename",
		UserInvocable:    true,
		Triggers:         []string{"go"},
		Instructions:     "do the thing",
		Timeout:          5 * time.Second,
		Budget:           42,
		InputSchema:      map[string]any{"type": "object"},
		OutputSchema:     map[string]any{"type": "object"},
		Tools:            []string{"run_command"},
		Resources:        []skills.ResourceDescriptor{{ID: "id", Summary: "summary"}},
	}
	out := CLISkillToSDK(in)
	// Only Name, Instructions, Triggers, RequiredTools are populated.
	if out.Name != "demo" {
		t.Fatalf("Name mismatch")
	}
	if out.Instructions != "do the thing" {
		t.Fatalf("Instructions mismatch")
	}
	if !reflect.DeepEqual(out.Triggers, []string{"go"}) {
		t.Fatalf("Triggers mismatch: %v", out.Triggers)
	}
	if !reflect.DeepEqual(out.RequiredTools, []string{"run_command"}) {
		t.Fatalf("RequiredTools mismatch: %v", out.RequiredTools)
	}
}

// TestCLISkillTriggersSplit confirms that the CLI's Triggers lands on
// the SDK's Triggers and the CLI's Tools lands on the SDK's
// RequiredTools. Field naming differs between the two shapes; a
// bridge that mapped both to the same SDK slice would silently drop
// one of the two halves.
func TestCLISkillTriggersSplit(t *testing.T) {
	in := skills.Definition{
		Name:         "demo",
		Instructions: "do the thing",
		Triggers:     []string{"build", "test"},
		Tools:        []string{"run_command", "read"},
	}
	out := CLISkillToSDK(in)
	if !reflect.DeepEqual(out.Triggers, []string{"build", "test"}) {
		t.Fatalf("CLI.Triggers did not land on SDK.Triggers: %v", out.Triggers)
	}
	if !reflect.DeepEqual(out.RequiredTools, []string{"run_command", "read"}) {
		t.Fatalf("CLI.Tools did not land on SDK.RequiredTools: %v", out.RequiredTools)
	}
}
