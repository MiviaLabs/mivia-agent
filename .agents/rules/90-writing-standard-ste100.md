# 90 - Writing standard (ASD-STE100, repo-adapted)

All agent-authored prose must follow this rule. This rule covers completion reports, findings, documentation, prompts, agent messages, and commit messages. It also covers code comments and doc comments. It does not cover code identifiers, quoted external text, or log output.

**Scope note**: This rule is a repo-adapted PARTIAL summary of ASD-STE100 (STE) Part 1 writing-style rules. The real standard has two parts. Part 1 has about 53 writing rules. Part 2 is a dictionary of about 900 approved words, each with one part of speech and one meaning. This repo does not enforce the Part 2 dictionary. Do not treat this file as a full STE100 implementation.

STE100 was written for human aircraft technicians who read static printed manuals. Most readers of this repo's prose are LLM agents. Apply the sentence-length and one-idea rules below with LLM clarity in mind. Do not treat word counts as a blind target.

Follow these rules:

- Write short sentences. Put one idea in each sentence.
- Use the active voice. Say "the tool writes the file", not "the file is written by the tool".
- Use the present tense for facts and procedures.
- Use one term for one thing. Do not use synonyms for the same concept.
- Use the same word for the same action.
- Use simple words. Avoid jargon, idioms, and metaphors.
- Give instructions in the imperative. Say "Run the test", not "you should run the test".
- Sentence length: keep instructional sentences (steps, commands) to about 20 words or fewer. Descriptive or procedural sentences (explaining a fact or a process) may run to about 25 words. This 20/25 split is the real STE100 rule; do not flatten it to a single number.
- Keep each paragraph to about 6 sentences or fewer. Split longer paragraphs.
- Prefer one clear sentence over several fragments when the idea is a single conditional or causal relationship (if/then, unless, because). Say "Run `make test` before you commit, unless the change is docs-only", not two disconnected sentences that force the reader to infer the link. The goal is clarity for an LLM reader, not the lowest sentence count.
- Use bullet lists and numbered steps for procedures.
- Do not use "-ing" forms where a simple form is correct. Say "to verify", not "verifying".
- Define abbreviations and acronyms before you use them.
- ASD-STE100 is the authority for the Part 1 style rules above. When you are in doubt about style, follow the specification. This file does not implement Part 2 (controlled vocabulary).
