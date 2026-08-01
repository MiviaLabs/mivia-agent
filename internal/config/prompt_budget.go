package config

// EffectiveOutputTokens returns the tighter positive model/session response
// ceiling. A nil result means neither layer configured a ceiling.
func EffectiveOutputTokens(profile ModelSpec, requested *int) *int {
	limit := profile.MaxOutputTokens
	if limit < 0 {
		limit = 0
	}
	if requested != nil && *requested > 0 && (limit == 0 || *requested < limit) {
		limit = *requested
	}
	if limit <= 0 {
		return nil
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
func EffectivePromptTokens(profile ModelSpec, maxTokens *int, operatorCap, requested int) int {
	capacity := profile.ContextWindowTokens
	if capacity <= 0 {
		capacity = maxContextWindowTokens
	}
	if reserve := EffectiveOutputTokens(profile, maxTokens); reserve != nil {
		capacity -= *reserve
	}
	if operatorCap > 0 && operatorCap < capacity {
		capacity = operatorCap
	}
	if requested > 0 && requested < capacity {
		capacity = requested
	}
	return capacity
}
