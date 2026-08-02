package chat

// RenameModel points a binding at a different model name without resolving a
// new profile, and drops the reasoning surface that belonged to the model being
// renamed away from.
//
// The reset is not cosmetic. An empty dialect does not mean "send no reasoning
// fields": provider.OpenAICompat falls back to the client's own default dialect
// (zai thinks, openrouter speaks openai), so a stale dial, dialect, or declared
// set puts reasoning fields on the wire for a model that never declared any.
// The declared set matters on its own - Session.SetReasoningEffort validates
// against it, so a stale set makes /effort accept a level the model does not
// offer.
//
// Every path that renames a selection in place must go through here (or through
// Session.renameModelLocked, which adds the session-scoped half) so the reset
// cannot drift between them.
func (b *ModelBinding) RenameModel(name string) {
	if name == b.Model {
		// Not a rename: the profile still describes this model, and wiping its
		// reasoning surface would silently disarm a model that does declare one.
		b.Profile.Name = name
		return
	}
	b.Model = name
	b.Profile.Name = name
	b.Profile.Reasoning = ""
	b.Profile.ReasoningDialect = ""
	b.Profile.ReasoningEfforts = nil
}

// renameModelLocked applies a rename to the published binding. The user's
// /effort choice goes with it: it was chosen for the previous model, and the
// new one may not offer that level at all.
func (s *Session) renameModelLocked(name string) {
	renamed := name != s.binding.Model
	s.binding.RenameModel(name)
	s.model = name
	if renamed {
		s.reasoningEffort = ""
	}
}
