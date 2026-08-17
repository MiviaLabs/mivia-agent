package skills

import "testing"

// FuzzParseFrontmatterNeverPanics drives the strict YAML-subset frontmatter
// parser with arbitrary bytes. ParseFrontmatter is a pure, bounded
// []byte -> (map[string]any, error) function with no IO or global state, so a
// crash here is a genuine parser defect. The seed corpus covers empty input,
// scalars, flow and block sequences, empty flow items, unbalanced quotes, and
// oversized input (> maxFrontmatterBytes); the fuzz engine explores every
// boundary from there.
func FuzzParseFrontmatterNeverPanics(f *testing.F) {
	seeds := []string{
		"",
		"no frontmatter here",
		"---\nname: review\n---",
		"---\nname: review\ndescription: Review code\n---\nbody",
		"---\ntriggers: [review, audit, check]\n---",
		"---\ntriggers:\n  - review\n  - audit\n---",
		"---\ntriggers: [a,]\n---",
		"---\ntriggers: [review, ]\n---",
		"---\ntriggers: [,]\n---",
		"---\ntriggers: [\"\"]\n---",
		"---\ntriggers: ['']\n---",
		"---\nname: 'unclosed\n---",
		"---\ntriggers: [\"a,b]\n---",
		"---\nname: \"\n---",
		"---\ntriggers:\n  - 'unclosed\n---",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	oversized := make([]byte, maxFrontmatterBytes+1)
	copy(oversized, []byte("---\nname: x\n"))
	for i := len("---\nname: x\n"); i < len(oversized); i++ {
		oversized[i] = ' '
	}
	f.Add(oversized)

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseFrontmatter(data)
	})
}

// FuzzParseSkillMarkdownNeverPanics drives the full skill markdown entry point
// (frontmatter parse + coercion fill) with arbitrary bytes. parseSkillMarkdown
// is a pure, bounded []byte -> (parsedSkill, error) function with no IO or
// global state, so a crash here is a genuine parser defect. The seed corpus
// mirrors the frontmatter fuzzer and adds flow-sequence user-invocable seeds
// that exercise the coercion fix (user-invocable: [false] parses as
// []string{"false"} and must refuse, not silently keep default true).
func FuzzParseSkillMarkdownNeverPanics(f *testing.F) {
	seeds := []string{
		"",
		"no frontmatter here",
		"---\nname: review\n---",
		"---\nname: review\ndescription: Review code\n---\nbody",
		"---\ntriggers: [review, audit, check]\n---",
		"---\ntriggers:\n  - review\n  - audit\n---",
		"---\ntriggers: [a,]\n---",
		"---\ntriggers: [review, ]\n---",
		"---\ntriggers: [,]\n---",
		"---\ntriggers: [\"\"]\n---",
		"---\ntriggers: ['']\n---",
		"---\nname: 'unclosed\n---",
		"---\ntriggers: [\"a,b]\n---",
		"---\nname: \"\n---",
		"---\ntriggers:\n  - 'unclosed\n---",
		"---\nname: x\nuser-invocable: [false]\n---\nbody",
		"---\nname: x\nuser-invocable: false\n---\nbody",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	oversized := make([]byte, maxFrontmatterBytes+1)
	copy(oversized, []byte("---\nname: x\n"))
	for i := len("---\nname: x\n"); i < len(oversized); i++ {
		oversized[i] = ' '
	}
	f.Add(oversized)

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseSkillMarkdown(data)
	})
}
