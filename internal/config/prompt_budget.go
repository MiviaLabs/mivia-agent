package config

import "github.com/MiviaLabs/mivia-agent/internal/reasoning"

// EffectiveOutputTokens returns the response allowance for one request: the
// completion size asked for on the wire, and the reserve subtracted from the
// context window to derive the prompt budget. Those two must stay in lockstep
// - providers validate input_tokens + max_tokens <= context_window - so this
// is the single place both are decided. A nil result means no ceiling applies.
//
// An EXPLICIT request ([chat] max_tokens) is authoritative up to the model's
// own ceiling. An UNSET request falls back to the model ceiling capped at
// max(DefaultOutputReserveTokens, reasoning.OutputReserveFloor(profile.Reasoning)),
// because a model's max_output_tokens is a per-response maximum rather than a
// sensible per-request default; see DefaultOutputReserveTokens for the
// prompt-budget damage the uncapped fallback caused. The reasoning.
// OutputReserveFloor term matters because the wire request layer
// (internal/provider's effectiveMaxTokens) applies that SAME floor as its own
// max_tokens fallback for an unset request - reserving only
// DefaultOutputReserveTokens here for a high-reasoning-effort model (e.g.
// z.ai's GLM-5.3 family at "max" effort, floor 65536) would let this budget
// pack history right up to a boundary the wire request then asks to exceed,
// risking a prompt_tokens+max_tokens over-context-window rejection this
// function has every input needed to avoid.
func EffectiveOutputTokens(profile ModelSpec, requested *int) *int {
	ceiling := profile.MaxOutputTokens
	if ceiling < 0 {
		ceiling = 0
	}
	if requested != nil && *requested > 0 {
		limit := *requested
		if ceiling > 0 && limit > ceiling {
			limit = ceiling
		}
		return clampReserveToWindow(profile, limit)
	}
	if ceiling <= 0 {
		// An undeclared ceiling does not mean the wire request will ask for
		// nothing: the wire layer (effectiveMaxTokens/anthropicMaxTokens)
		// applies reasoning.OutputReserveFloor unconditionally whenever
		// MaxTokens is unset, with NO dependency on profile.MaxOutputTokens -
		// it reads only the request's resolved reasoning level. A model
		// entry with an active reasoning level but no max_output_tokens
		// (shipped examples: deepseek-v4-pro, gpt-oss:20b, tencent/hy3-preview
		// in .mivia/mivia.toml) would otherwise reserve 0 prompt-budget room
		// while the wire layer still asks for up to
		// reasoning.OutputReserveFloor(profile.Reasoning) tokens. A profile
		// with no reasoning configured at all keeps the prior nil ("no
		// ceiling applies") behavior unchanged - this only closes the gap
		// for the case that is actually reachable and actually mismatched.
		if !profile.Reasoning.Active() {
			return nil
		}
		return clampReserveToWindow(profile, reasoning.OutputReserveFloor(profile.Reasoning))
	}
	limit := DefaultOutputReserveTokens
	if floor := reasoning.OutputReserveFloor(profile.Reasoning); floor > limit {
		limit = floor
	}
	if limit > ceiling {
		limit = ceiling
	}
	return clampReserveToWindow(profile, limit)
}

// clampReserveToWindow keeps the response allowance inside the declared
// context window. A reserve larger than the whole window is unsatisfiable:
// providers validate input_tokens + max_tokens <= context_window, so such a
// request is rejected outright rather than merely leaving no prompt room.
// Validated config never reaches this (load rejects windows at or below the
// reserve), so this guards hand-built profiles and explicit operator requests.
func clampReserveToWindow(profile ModelSpec, limit int) *int {
	if profile.ContextWindowTokens > 0 && limit > profile.ContextWindowTokens {
		limit = profile.ContextWindowTokens
	}
	return &limit
}

func promptCap(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// EffectivePromptTokens computes the local prompt budget after reserving the
// configured completion allowance and applying an optional operator/session cap.
//
// The returned budget is never negative. When the completion reserve consumes
// or exceeds the whole context window, the budget is 0: the guard fails closed
// and the provider-side prompt-too-long recovery is the backstop. Validated
// config cannot reach that state - load rejects context_window_tokens below
// minContextWindowTokens (1024) and windows at or below the max_tokens
// reserve - so 0 only surfaces for hand-constructed profiles in tests and
// runtime bindings.
//
// A non-positive ContextWindowTokens is the undeclared/unrecognized window
// fallback: it fails closed at UnknownContextWindowTokens (128k). Validated config
// also never reaches that branch (load rejects windows below 1024); it exists
// for hand-constructed profiles in tests and legacy runtime bindings.
func EffectivePromptTokens(profile ModelSpec, maxTokens *int, operatorCap, requested int) int {
	capacity := profile.ContextWindowTokens
	if capacity <= 0 {
		capacity = UnknownContextWindowTokens
	}
	if reserve := EffectiveOutputTokens(profile, maxTokens); reserve != nil {
		capacity -= *reserve
	}
	// Clamp at 0: a reserve >= window leaves no prompt capacity. Returning a
	// negative budget would surface as a "budget must be positive" plan
	// failure or silently disable the pruning guard.
	if capacity < 0 {
		capacity = 0
	}
	if operatorCap > 0 && operatorCap < capacity {
		capacity = operatorCap
	}
	if requested > 0 && requested < capacity {
		capacity = requested
	}
	return capacity
}
