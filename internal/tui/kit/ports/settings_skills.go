package ports

import "context"

// SkillView is one skill definition from the active skill registry.
type SkillView struct {
	Name              string
	Description       string
	Origin            string // "project" | "user"
	Scope             string
	Tools             []string
	ArgsHint          string
	UserInvocable     bool
	Triggers          []string
	InstructionsChars int
	FilePath          string
	Instructions      string
}

// SkillEdit is a closed union of skill mutations.
type SkillEdit interface{ isSkillEdit() }

// RemoveSkill removes a skill from disk in the specified scope.
type RemoveSkill struct {
	Name   string
	Origin string // "project" | "user"
}

// SetSkillUserInvocable updates the user-invocable flag of a skill.
type SetSkillUserInvocable struct {
	Name   string
	Origin string
	On     bool
}

// SaveSkill saves or updates skill frontmatter and instructions.
type SaveSkill struct {
	Name          string
	Origin        string
	Description   string
	UserInvocable bool
	Tools         []string
	Triggers      []string
	Instructions  string
}

func (RemoveSkill) isSkillEdit()           {}
func (SetSkillUserInvocable) isSkillEdit() {}
func (SaveSkill) isSkillEdit()             {}

// SkillSettings is the Skills section's read/write surface.
type SkillSettings interface {
	Skills() []SkillView
	Apply(ctx context.Context, scope Scope, e SkillEdit) (SaveHandle, error)
}
