package sdkadapter

import (
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/skills"
)

// SDKSkillToCLI converts an SDK-shaped Skill into the CLI's
// skills.Definition. The SDK carries four fields the CLI cares about:
// Name, Instructions, Triggers, RequiredTools. RequiredTools maps onto
// the CLI's Tools field.
//
// The CLI Definition carries 13 product-layer fields the SDK cannot
// represent (Version, Scope, Origin, Permission, Description,
// ShortDescription, ArgsHint, UserInvocable, Timeout, Budget,
// InputSchema, OutputSchema, Resources). They are deliberately not
// surfaced; populating them with defaults would invent behaviour the
// SDK did not opt into.
func SDKSkillToCLI(s sdkshape.Skill) skills.Definition {
	return skills.Definition{
		Name:         s.Name,
		Instructions: s.Instructions,
		Triggers:     s.Triggers,
		Tools:        s.RequiredTools,
	}
}

// CLISkillToSDK converts a CLI Definition into the SDK-shaped Skill.
// The reverse direction only carries the four fields the SDK can
// store; the 13 product-layer fields are dropped (see SDKSkillToCLI
// for the rationale).
//
// Field mapping:
//   - CLI Triggers -> SDK Triggers.
//   - CLI Tools -> SDK RequiredTools.
//   - CLI Name, Instructions -> SDK Name, Instructions verbatim.
func CLISkillToSDK(d skills.Definition) sdkshape.Skill {
	return sdkshape.Skill{
		Name:          d.Name,
		Instructions:  d.Instructions,
		Triggers:      d.Triggers,
		RequiredTools: d.Tools,
	}
}
