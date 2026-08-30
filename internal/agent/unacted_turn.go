// Package agent - bounded continuation for a turn that announced work and
// then ended without doing any of it.
//
// The failure this exists for: a model answers "I am going to dispatch four
// agents", emits no tool call, and the loop ends the turn correctly - the
// response really did say stop. The user then has to send another message
// to get the work that was already promised. It is a model-side behaviour,
// not a transport defect, and it varies by model, which is exactly why the
// remedy belongs in the host loop where every provider passes through, not
// in one provider adapter.
//
// It sits beside retryOnEmptyResponse (agentloop_run.go), which covers the
// neighbouring case: a turn with no text AND no tool calls. The two never
// overlap - that one requires empty text, this one requires non-empty text.

package agent

import (
	"context"
	"fmt"
	"strings"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// unactedContinuationNotice is the model-facing nudge. It states the
// observation (no tool ran) and leaves every branch open, including the
// branch where the model was right to stop: a nudge that demanded action
// would push a model into inventing work it does not need to do.
//
// It is project- and language-generic by rule: no tool name, no language,
// no repository layout.
// The bracket label is required, not cosmetic. The notice is appended as a
// RoleUser message (RoleSystem is only valid at index 0, and RoleUser is how
// this loop already injects host notices - see promptTooLongCompactNotice),
// and the whole run history is written back to the session, so this sentence
// PERSISTS and replays on every later turn. Unlabelled, the model would read
// host prose as the user's own words for the rest of the session - the weak
// form of DC-23. The label says who is speaking.
const unactedContinuationNotice = "[mivia: your previous message described work that was not performed - no tool ran during that turn. " +
	"If the work is still needed, carry it out now using the tools available. " +
	"If it is not needed, or you need an answer from the user first, say so plainly instead.]"

// continueUnactedTurn re-runs a turn that announced work and then ended
// without calling a single tool, up to opts.MaxUnactedContinuations times.
//
// Every precondition is load-bearing:
//   - MaxUnactedContinuations > 0. The whole mechanism is opt-in: a nudge
//     costs a second provider call, and whether a model needs one is a
//     property of the model, so an operator decides, not this code.
//   - DisableProviderReplay off. A continuation IS a replay, and the flag
//     suppresses the sibling empty-response retry for the same reason.
//   - No error, and the stop is exactly StopNoToolCalls. Any other stop
//     (max iterations, hook veto, steered, repeated failures, concluded)
//     has its own meaning and must not be re-driven.
//   - Zero tool calls in the whole turn. This is what makes a continuation
//     safe rather than a duplicate-work risk: nothing ran, so nothing can
//     run twice. A turn that called one tool and then narrated the next is
//     deliberately NOT continued.
//   - Tools were advertised. With no tool surface there is no work the
//     model could have done, so the turn was complete by construction.
//   - Non-empty final text that reads as an unperformed intent (see
//     announcesUnactedWork).
//
// The continuation appends the assistant's own message and the notice to
// the run's history rather than re-asking the original prompt: the model
// keeps what it said, so it continues from its plan instead of restarting.
func continueUnactedTurn(ctx context.Context, l *Loop, sdkOpts sdkagentloop.Options, opts Options, turn *sdkTurnState, turnUserText string, res sdkagentloop.Result, err error) (sdkagentloop.Result, error) {
	if opts.MaxUnactedContinuations <= 0 || opts.DisableProviderReplay {
		return res, err
	}
	for attempt := 0; attempt < opts.MaxUnactedContinuations; attempt++ {
		if !turnLeftWorkUnacted(sdkOpts, opts, turnUserText, res, err) {
			return res, err
		}
		emit(opts, Event{
			Kind:   EventUnactedContinuation,
			Detail: fmt.Sprintf("turn announced work but called no tool, continuing (%d/%d)", attempt+1, opts.MaxUnactedContinuations),
		})
		// The announcement is optimistic content: text the model streamed
		// live, immediately before doing the work it described. That is the
		// same shape sdk_tool_events.go revokes when a tool call arrives
		// after streamed text, and it must be revoked here for the same
		// reason plus one more. If the continued run comes back with a zero
		// Final (StopEmptyResponse, StopMaxIterations), finalizeSDKTurn
		// falls back to "the last assistant text anywhere in the turn",
		// finds this announcement, and writes it to FinalWriter - which
		// already received it live. The user would read the same sentence
		// twice. Revoking is a no-op when nothing was streamed or the
		// writer does not implement streamRevoker.
		revokeStreamWriter(opts.FinalWriter)
		res, err = runSDKPromptTooLongRecoverable(ctx, l, sdkOpts, opts, continuedHistory(res.History), turn)
	}
	return res, err
}

// turnLeftWorkUnacted reports whether one completed run is the
// announced-but-unperformed shape. Split out from the loop so the retry
// bound and the predicate can be read (and tested) separately.
func turnLeftWorkUnacted(sdkOpts sdkagentloop.Options, opts Options, turnUserText string, res sdkagentloop.Result, err error) bool {
	if err != nil || res.Stop != sdkagentloop.StopNoToolCalls {
		return false
	}
	if !runAdvertisedTools(sdkOpts, opts) {
		return false
	}
	if sdkTurnMadeToolCalls(res.History, turnUserText) {
		return false
	}
	return announcesUnactedWork(sdkResolvedFinalText(res, turnUserText))
}

// runAdvertisedTools reports whether the run offered the model any tool at
// all. The pinned advertised snapshot wins when the host set one (it is
// what actually reaches the wire, deferred entries included); otherwise the
// SDK registry's own definitions decide.
func runAdvertisedTools(sdkOpts sdkagentloop.Options, opts Options) bool {
	if opts.AdvertisedToolSpecs != nil {
		return len(opts.AdvertisedToolSpecs) > 0
	}
	return sdkOpts.Tools != nil && len(sdkOpts.Tools.Tools()) > 0
}

// continuedHistory returns the run history with the notice appended as a
// user turn. The copy is private: the caller's Result keeps its own slice,
// and the SDK loop appends to whatever it is handed.
func continuedHistory(history []sdkshape.Message) []sdkshape.Message {
	out := make([]sdkshape.Message, 0, len(history)+1)
	out = append(out, history...)
	return append(out, sdkshape.Message{
		Role:    sdkshape.RoleUser,
		Content: unactedContinuationNotice,
	})
}

// intentPhrases are first-person statements of work about to be done.
// Second person ("you should run") and past tense ("I ran") are absent on
// purpose: neither is a promise this turn left unkept.
var intentPhrases = []string{
	"i'll", "i will", "i am going to", "i'm going to", "let me",
	"i plan to", "i need to", "i should", "next i", "now i",
	"my next step", "the next step is to",
}

// actionVerbs name work that requires a tool. A phrase that promises only
// prose ("I will explain", "let me summarize") is not unacted work.
var actionVerbs = []string{
	"spawn", "dispatch", "delegate", "launch", "start", "run", "execute",
	"call", "invoke", "use", "read", "open", "write", "edit", "create",
	"delete", "search", "grep", "look", "inspect", "examine", "check",
	"analyze", "analyse", "review", "audit", "fix", "update", "apply",
	"implement", "add", "remove", "refactor", "test", "verify", "build",
	"commit", "load", "fetch", "list", "find", "explore", "investigate",
}

// intentProximity bounds how far after an intent phrase an action verb
// still counts as part of the same promise. It is a sentence-scale window:
// wide enough for "I'll now go ahead and dispatch", narrow enough that an
// unrelated later sentence does not pair with an earlier "let me".
const intentProximity = 80

// announcesUnactedWork reports whether an assistant message reads as a
// promise of tool work.
//
// This is a deliberate heuristic and the only inexact part of the
// mechanism, which is why the whole feature is opt-in. It is
// English-oriented and pattern-based; it will miss promises in other
// languages and in unusual phrasing. A false negative costs nothing - the
// turn ends exactly as it does today. A false positive costs one extra
// provider call whose notice explicitly permits the model to answer "no
// further work is needed", so it cannot fabricate work by itself.
//
// A message that ends in a question, or that defers to the user, is never
// continued: the model handed the decision back, and nudging past it would
// take an unapproved action on the user's behalf. That is the one false
// positive that costs more than a wasted provider call, so deferral is
// checked before intent and wins outright.
func announcesUnactedWork(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.HasSuffix(trimmed, "?") {
		return false
	}
	lower := strings.ToLower(trimmed)
	if containsAnyPhrase(lower, deferralPhrases) {
		return false
	}
	for _, phrase := range intentPhrases {
		if followedByAction(lower, phrase) {
			return true
		}
	}
	return false
}

// deferralPhrases mark text that hands the decision back to the user. They
// are checked first and suppress the whole message, because they collide
// head-on with the intent lexicon: "let me know if you'd like me to run the
// tests" contains "let me" and "run" within the proximity window, and
// "I need to check with you first" contains "i need to" and "check". Acting
// on either would run a tool the model deliberately did not run.
var deferralPhrases = []string{
	"let me know", "would you like", "do you want", "if you'd like",
	"if you like", "if you want", "with you first", "check with you",
	"confirm with you", "your call", "shall i", "want me to",
	"tell me if", "let me know if", "waiting for your", "before proceeding",
	"before i proceed", "your approval", "your permission", "say the word",
}

// containsAnyPhrase reports whether lower contains any of phrases.
func containsAnyPhrase(lower string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// followedByAction reports whether any occurrence of phrase in lower is
// followed by an action verb within intentProximity bytes.
func followedByAction(lower, phrase string) bool {
	for offset := 0; ; {
		index := strings.Index(lower[offset:], phrase)
		if index < 0 {
			return false
		}
		start := offset + index + len(phrase)
		end := min(start+intentProximity, len(lower))
		if containsAnyWord(lower[start:end], actionVerbs) {
			return true
		}
		offset = start
	}
}

// containsAnyWord reports whether window contains any of words as a whole
// word. Substring matching would pair "list" with "realistic" and "add"
// with "additionally".
func containsAnyWord(window string, words []string) bool {
	for _, field := range strings.FieldsFunc(window, func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	}) {
		for _, word := range words {
			if field == word {
				return true
			}
		}
	}
	return false
}
