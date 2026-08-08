// Package prompts holds prompt fragments that the compiled-in system prompts
// share. A fragment lives here when more than one prompt must state the same
// rule, so the rule has one source and cannot drift between prompts.
package prompts

// WritingStandard is the default style for all prose that an agent writes.
//
// The standard is ASD-STE100 Simplified Technical English. STE is a controlled
// form of English: it limits sentence length, vocabulary, and grammar so that
// a reader who is not a native speaker can read the text without ambiguity.
//
// This fragment is a STYLE rule, not a project rule. It says how to write, not
// what to write about, and it names no language, framework, or repository. It
// is therefore safe in the compiled-in defaults that rule 60 requires to stay
// project- and language-generic.
//
// The list is short on purpose. Every compiled prompt carries these bytes on
// every model call, so each line must earn its cost. The lines here are the
// STE rules that change agent output the most. AGENTS.md holds the full set.
const WritingStandard = `# Writing standard (ASD-STE100)
Write all prose in ASD-STE100 Simplified Technical English: reports, findings,
documentation, commit messages, pull-request titles and summaries, agent
messages, and code comments. Not code identifiers, quoted text, or log output.
- Short sentences. One idea in each. 20 words or fewer.
- Active voice. Present tense. Imperative for an instruction.
- One term for one thing. No synonym, jargon, idiom, or metaphor.
- Simple words. No "-ing" form when a simple form is correct ("to verify").
- Write out each abbreviation the first time you use it.`
