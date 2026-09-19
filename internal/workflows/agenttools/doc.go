// Package agenttools exposes the in-process workflow tools for the agent
// surface: the eight workflow_* tools, their shared Service, the Engine
// seam for mutations, and workflow discovery. Tools read through the
// workflow ledger (internal/workflows/ledger) and mutate through an
// injected Engine. This package must not import controller/agents/skills
// so the tools wrapper package can import it without an import cycle.
package agenttools
