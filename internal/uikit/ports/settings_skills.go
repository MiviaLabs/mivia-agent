package ports

// SkillView is one skill definition from the active skill registry.
type SkillView struct {
	Name              string
	Description       string
	Origin            string // "project" | "user" | "system"
	Scope             string
	Tools             []string
	ArgsHint          string
	UserInvocable     bool
	Triggers          []string
	InstructionsChars int
}

// SkillSettings is the Skills section's read surface.
type SkillSettings interface {
	Skills() []SkillView
}
