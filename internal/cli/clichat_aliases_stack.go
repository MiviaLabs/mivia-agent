package cli

// clichat_aliases.go re-exports symbols that moved to internal/clichat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"

// FilterSkillsForScope re-exports the clichat.FilterSkillsForScope function.
var FilterSkillsForScope = clichat.FilterSkillsForScope

// KeyScope re-exports the clichat.KeyScope type.
type KeyScope = clichat.KeyScope

// ScopeComposer re-exports the clichat.ScopeComposer variable.
var ScopeComposer = clichat.ScopeComposer

// ScopeDashboard re-exports the clichat.ScopeDashboard variable.
var ScopeDashboard = clichat.ScopeDashboard

// ScopeGlobal re-exports the clichat.ScopeGlobal variable.
var ScopeGlobal = clichat.ScopeGlobal

// ScopeHistory re-exports the clichat.ScopeHistory variable.
var ScopeHistory = clichat.ScopeHistory

// ScopeOverlay re-exports the clichat.ScopeOverlay variable.
var ScopeOverlay = clichat.ScopeOverlay

// ScopeQueue re-exports the clichat.ScopeQueue variable.
var ScopeQueue = clichat.ScopeQueue

// ScopeScrollback re-exports the clichat.ScopeScrollback variable.
var ScopeScrollback = clichat.ScopeScrollback

// ScopeSessions re-exports the clichat.ScopeSessions variable.
var ScopeSessions = clichat.ScopeSessions

// ScopeSuggest re-exports the clichat.ScopeSuggest variable.
var ScopeSuggest = clichat.ScopeSuggest

// ScopeWelcome re-exports the clichat.ScopeWelcome variable.
var ScopeWelcome = clichat.ScopeWelcome

// ScopeWorkflows re-exports the clichat.ScopeWorkflows variable.
var ScopeWorkflows = clichat.ScopeWorkflows

// SkillScopeFromAgent re-exports the clichat.SkillScopeFromAgent function.
var SkillScopeFromAgent = clichat.SkillScopeFromAgent
