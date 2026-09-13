// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (skillstep.go) implements the StepSkill dispatch: parsing a
// step's Ref into a skill name and its trailing argument text,
// resolving that name against a live *skills.Registry using the same
// matching semantics as the interactive TUI's own skill-slash
// resolution (internal/uiadapter/runner.go's handleSkill), rendering
// the skill's full instructions, and sending them as one headless turn
// whose PersistedText is the short slash command rather than the
// rendered instructions. Before this file, StepSkill sent the literal
// "/"+Ref text as a prompt - a plain string the model has no skill
// content behind, and which doubled its leading slash whenever an
// already-slashed Ref reached the executor.
package automation

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// parseSkillRef splits a StepSkill step's Ref into the skill name and
// its trailing argument text. Every leading "/" is stripped (not just
// one), so a ref carrying an already-slashed value - including the
// historic double-slash this package's old "/"+Ref send produced -
// still resolves to the intended skill name rather than failing lookup.
// Only the leading run of slashes before the name is stripped: a slash
// inside the argument text (a file path, for example) is left
// untouched. An empty or whitespace-only ref is rejected.
func parseSkillRef(ref string) (name, args string, err error) {
	trimmed := strings.TrimSpace(ref)
	trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "/"))
	if trimmed == "" {
		return "", "", fmt.Errorf("automation: skill ref is empty")
	}
	if idx := strings.IndexFunc(trimmed, unicode.IsSpace); idx >= 0 {
		return trimmed[:idx], strings.TrimSpace(trimmed[idx:]), nil
	}
	return trimmed, "", nil
}

// resolveSkillDefinition looks up name in reg using the same matching
// semantics as internal/uiadapter/runner.go's handleSkill: a match via
// skills.SlashToken(def.Name) OR a case-insensitive equality against
// def.Name. A resolved definition that is not UserInvocable is refused
// for the same reason handleSkill refuses one - a skill not meant for
// direct invocation must not become directly invocable just because an
// automation step names it.
func resolveSkillDefinition(reg *skills.Registry, name string) (skills.Definition, error) {
	if reg == nil {
		return skills.Definition{}, fmt.Errorf("automation: no skill registry available to resolve skill %q", name)
	}
	cleanName := strings.ToLower(strings.TrimSpace(name))
	for _, def := range reg.List() {
		token, ok := skills.SlashToken(def.Name)
		bareToken := strings.TrimPrefix(token, "/")
		if (ok && bareToken == cleanName) || strings.EqualFold(def.Name, cleanName) {
			if !def.UserInvocable {
				return skills.Definition{}, fmt.Errorf("automation: skill %q cannot be invoked directly", name)
			}
			return def, nil
		}
	}
	return skills.Definition{}, fmt.Errorf("automation: skill %q is not recognized", name)
}

// validateStepSkill implements ValidateSpec's StepSkill guard: an
// empty/whitespace ref is rejected outright, reusing parseSkillRef's own
// validation; when registry is non-nil, the parsed name must also
// resolve (resolveSkillDefinition) - mirroring validateStepSlash's own
// nil-registry-means-unchecked contract in slashallow.go.
func validateStepSkill(automationID string, stepIndex int, ref string, registry *skills.Registry) error {
	name, _, err := parseSkillRef(ref)
	if err != nil {
		return fmt.Errorf("automation %q: step %d: %w", automationID, stepIndex, err)
	}
	if registry == nil {
		return nil
	}
	if _, err := resolveSkillDefinition(registry, name); err != nil {
		return fmt.Errorf("automation %q: step %d: %w", automationID, stepIndex, err)
	}
	return nil
}

// skillRegistryFor resolves the *skills.Registry a StepSkill dispatch
// should resolve against: boundSess's own current binding first (the
// live registry the spawned session started with - the same source
// internal/uiadapter/runner.go's skillRegistry() reads via
// sess.CurrentBinding().SkillRegistry), falling back to the Service's
// configured registry source (Config.SkillRegistry) when the binding
// carries none. Both may legitimately be nil - resolveSkillDefinition
// reports that as its own "no skill registry available" error rather
// than this function panicking or guessing.
func (s *Service) skillRegistryFor(boundSess *chat.Session) *skills.Registry {
	if boundSess != nil {
		if reg := boundSess.CurrentBinding().SkillRegistry; reg != nil {
			return reg
		}
	}
	return s.configSkillRegistry()
}

// configSkillRegistry calls Config.SkillRegistry when set, or returns
// nil. Factored out of skillRegistryFor so upsert (service.go) can reuse
// the identical "call it and use the result, may be nil" resolution for
// ValidateSpec without needing a *chat.Session of its own.
func (s *Service) configSkillRegistry() *skills.Registry {
	if s.cfg.SkillRegistry == nil {
		return nil
	}
	return s.cfg.SkillRegistry()
}

// skillPersistedText is what a StepSkill dispatch persists into the
// session's own history in place of the rendered instructions -
// mirroring internal/uiadapter/runner.go's skillInvocationText: the
// short slash command an automation author actually wrote, not the
// (typically much longer) expanded SKILL.md body every later turn would
// otherwise replay.
func skillPersistedText(name, args string) string {
	token, ok := skills.SlashToken(name)
	if !ok {
		token = "/" + name
	}
	if args != "" {
		return token + " " + args
	}
	return token
}

// sendSkillStep implements the StepSkill dispatch: parse ref, resolve it
// against skillRegistryFor(boundSess), render the skill's full
// instructions (skills.RenderNamedSkillSlashPrompt), and send them as
// one headless turn (sendIntentHeadless, headless.go) whose
// PersistedText is the short slash command rather than the rendered
// instructions. workDir is accepted for call-shape symmetry with the
// executor's other per-step dispatch helpers (runWorkflowStep); a
// StepSkill dispatch itself needs no filesystem location of its own.
func (s *Service) sendSkillStep(ctx context.Context, automationID string, stepIndex int, workDir string, conv ports.Conversation, boundSess *chat.Session, ref string, timeout time.Duration) error {
	_ = workDir
	name, args, err := parseSkillRef(ref)
	if err != nil {
		return wrapStepErr(automationID, stepIndex, err)
	}
	def, err := resolveSkillDefinition(s.skillRegistryFor(boundSess), name)
	if err != nil {
		return wrapStepErr(automationID, stepIndex, err)
	}
	rendered := skills.RenderNamedSkillSlashPrompt(def.Name, def.Instructions, args)
	in := intent.Send{Text: rendered, PersistedText: skillPersistedText(def.Name, args)}
	_, err = sendIntentHeadless(ctx, conv, in, timeout)
	return wrapStepErr(automationID, stepIndex, err)
}
