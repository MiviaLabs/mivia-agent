package cli

// clichat_aliases.go re-exports symbols that moved to internal/cli/chat so
// staying consumers (internal/legacytui) compile without per-file import
// updates. Use the clichat-qualified form in new code. These aliases are
// intentional shims while the extraction stabilises.

import clichat "github.com/MiviaLabs/mivia-agent/internal/cli/chat"

// ClearSubagentProgress re-exports the clichat.ClearSubagentProgress function.
var ClearSubagentProgress = clichat.ClearSubagentProgress

// RegisterSessionBus re-exports the clichat.RegisterSessionBus function.
var RegisterSessionBus = clichat.RegisterSessionBus

// SetSubagentProgress re-exports the clichat.SetSubagentProgress function.
var SetSubagentProgress = clichat.SetSubagentProgress

// ContextStorePath re-exports the clichat.ContextStorePath function.
var ContextStorePath = clichat.ContextStorePath

// OpenContextStorePath re-exports the clichat.OpenContextStorePath function.
var OpenContextStorePath = clichat.OpenContextStorePath

// StepTimeout re-exports the clichat.StepTimeout helper.
var StepTimeout = clichat.StepTimeout
