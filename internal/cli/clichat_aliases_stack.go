package cli

// clichat_aliases.go re-exports symbols that moved to internal/cli/chat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/cli/chat"

// FilterSkillsForScope re-exports the clichat.FilterSkillsForScope function.
var FilterSkillsForScope = clichat.FilterSkillsForScope

// SkillScopeFromAgent re-exports the clichat.SkillScopeFromAgent function.
var SkillScopeFromAgent = clichat.SkillScopeFromAgent
