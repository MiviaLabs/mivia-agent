// Package definition is the compiled form of a workflow: the parsed TOML
// shape, the catalogue of declared verifier profiles behind evidence_gate
// steps, and the closed structural matcher for step transitions.
//
// The host ships no built-in verifier profiles: the catalogue is filled from
// workspace config ([verifiers] in mivia.toml), so the engine stays project-
// and language-generic. Workflow files may name a declared profile only; they
// cannot supply shell or command strings. One exception: an evidence_gate
// step may declare a sandboxed command (a bare executable name plus argv,
// never a shell string) that runs inside the same isolation as the declared
// profiles: a copied worktree without secrets, no network, no host home, and
// an empty environment.
//
// The matcher evaluates attempt status plus exact scalar and enum output
// fields only. It is not an expression language: no regex, arithmetic,
// negation, or prose.
//
// It may import internal/agents, internal/skills, internal/jschema,
// internal/secretpath, internal/redact, internal/textutil, and
// internal/workspace. The other internal/workflows packages may import it.
package definition
