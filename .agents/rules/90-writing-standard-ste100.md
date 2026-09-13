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

## LLM style tells to avoid

STE100 does not cover these patterns. This repo adds them. They are the default habits of LLM prose. They add words that carry no fact.

Apply this section to the same scope as the rules above. These are habits to break, not banned strings. If the content needs the construction, use it. Do not contort a sentence to avoid the list.

Do not write:

- A contrast frame for emphasis, such as "it is not X, it is Y" or "this is not just X, it is Y". Use it only to correct a specific wrong belief.
- A three-item list that you chose for rhythm. Use the number of items the content has.
- A rhetorical transition question, such as "The catch?" or "But here is the thing:".
- A filler participle that states an obvious link, such as "highlighting", "emphasizing", "underscoring", "showcasing", "facilitating", or "leveraging".
- A corporate word when a plain word is correct. Write "use", not "utilize". Write "run", not "execute on".
- An unmeasured praise adjective, such as "robust", "seamless", "powerful", "comprehensive", "crucial", or "pivotal". Give the number, the gate, or the failure that the change prevents. If you have neither, remove the word.
- A hedge with no recommendation, such as "it is worth considering". Name the option you recommend and give the reason.
- A vague attribution, such as "experts note" or "studies show". Give the file, the command, the commit, or the URL.
- A number, a benchmark, or a date with no source. Give the command or the file that produced it. If you have no source, remove the number.
- An invented name, company, quote, or example record. Use real names from the repo, or use none.
- A repeated opener, such as several sentences that start with the same clause, "whether ... or", or "from ... to ...".
- A formulaic opener or closer, such as "In today's fast-paced world" or a "challenges and future prospects" section.
- A fixed count of bullets per section, such as exactly five.
- "the tool", "the system", or "the framework" when the real name is known. Write the name.
- "quiet", "quietly", or "silently" as narration of normal behavior.
- Emoji as decoration.

Also do these:

- Put the answer first. Then give the reasoning the reader needs.
- Point to a file, a line, or a command instead of a description of it.
- Remove each sentence that does not change what the reader does next.

### Em dashes

Use a normal hyphen, a comma, a colon, or a full stop. Do not use an em dash as a default clause splicer. This rule is the single source for that policy; `.agents/doctrines/engineering-working-contract.md` points here.

Two exceptions stay valid:

- A table cell or a placeholder where a documented format already uses `—`. The workflow run status table is one example. See `.agents/memories/workflow-run-status-table-format.md`.
- Quoted external text, which this rule never rewrites.

### Sentence cadence

Do not add a "vary your sentence length" rule to this repo's prose. It contradicts the STE100 rules above, which ask for short sentences, one term for one thing, and the same word for the same action. Consistency wins in controlled technical prose.

### Limits of this section

These patterns are probabilistic style tells. They are not proof that a text came from a model. Humans classify AI text at close to chance level, and the same patterns occur in human writing. Sources: [Wikipedia:Signs of AI writing](https://en.wikipedia.org/wiki/Wikipedia:Signs_of_AI_writing) and Kobak et al., "Delving into LLM-assisted writing in biomedical publications through excess vocabulary" ([arXiv:2406.07016](https://arxiv.org/abs/2406.07016)), which measures the excess vocabulary that this section names.

Use this section to write better text. Do not use it to accuse an author.

### Enforcement status

No gate checks this section today. It is advisory policy that a reviewer applies. To make one class mechanical, use the `gate-authoring` skill.
