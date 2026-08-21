package legacytui

func (m *TUIModel) suggestComposerTop() int {
	// The live panel is an overlay and holds no layout band, so the composer
	// sits directly below the full-height viewport.
	return 1 + m.viewport.Height
}
