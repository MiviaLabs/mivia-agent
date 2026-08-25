package config

// UpdateChatNoticeConfig updates or sets the show_iteration_notices and
// show_prompt_cache_notices keys under [chat] in the TOML config file at
// path. Locked and atomic (see updateConfigFile in persist_lock.go) so a
// concurrent edit to the same file from another goroutine cannot silently
// lose either write - this mutator previously used its own bespoke
// read-marshal-write with no synchronization or atomic rename at all,
// unlike every other config mutator in this package.
func UpdateChatNoticeConfig(path string, showIteration, showPromptCache bool) error {
	return updateConfigFile(path, func(raw map[string]any) error {
		chatRaw, ok := raw["chat"].(map[string]any)
		if !ok || chatRaw == nil {
			chatRaw = make(map[string]any)
		}
		chatRaw["show_iteration_notices"] = showIteration
		chatRaw["show_prompt_cache_notices"] = showPromptCache
		raw["chat"] = chatRaw
		return nil
	})
}
